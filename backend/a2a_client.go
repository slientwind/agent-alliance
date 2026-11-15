package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/a2aproject/a2a-go-v1/a2a"
	"github.com/a2aproject/a2a-go-v1/a2aclient"
	"github.com/a2aproject/a2a-go-v1/a2acompat/a2av0"
	grpcv0 "github.com/a2aproject/a2a-go-v1/a2agrpc/v0"
	grpcv1 "github.com/a2aproject/a2a-go-v1/a2agrpc/v1"
	"github.com/a2aproject/a2a-go-v1/a2asrv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type RemoteTask struct {
	ID        a2a.TaskID      `json:"id"`
	ContextID string          `json:"contextId"`
	State     a2a.TaskState   `json:"state"`
	Artifacts []*a2a.Artifact `json:"artifacts,omitempty"`
}
type remoteCheckpointKey struct{}
type remoteCheckpoint struct {
	CanCancel func() bool
	Previous  *RemoteTask
	Save      func(*RemoteTask) error
}
type agentAuthInterceptor struct {
	a2aclient.PassthroughInterceptor
	headers map[string]string
}

func (i agentAuthInterceptor) Before(ctx context.Context, r *a2aclient.Request) (context.Context, any, error) {
	for key, value := range i.headers {
		r.ServiceParams.Append(key, value)
	}
	if key, ok := ctx.Value(invocationKey{}).(string); ok {
		r.ServiceParams.Append("Idempotency-Key", key)
	}
	return ctx, nil, nil
}
func sdkCard(c *Card) (*a2a.AgentCard, error) {
	copy := *c
	if copy.PreferredTransport == "" && copy.URL != "" {
		copy.PreferredTransport = "JSONRPC"
	}
	b, e := json.Marshal(copy)
	if e != nil {
		return nil, e
	}
	var card *a2a.AgentCard
	if len(c.Extra["supportedInterfaces"]) > 0 && !strings.HasPrefix(c.ProtocolVersion, "0.3") {
		card = &a2a.AgentCard{}
		e = json.Unmarshal(b, card)
	} else {
		card, e = a2av0.NewAgentCardParser()(b)
	}
	if e != nil {
		return nil, e
	}
	if len(card.SupportedInterfaces) == 0 {
		return nil, fmt.Errorf("Agent Card 缺少可用 interface")
	}
	return card, nil
}
func negotiateAuth(card *a2a.AgentCard, secret string) (map[string]string, error) {
	if len(card.SecurityRequirements) == 0 {
		if secret != "" {
			return map[string]string{"Authorization": "Bearer " + secret}, nil
		}
		return map[string]string{}, nil
	}
	for _, option := range card.SecurityRequirements {
		headers := map[string]string{}
		supported := true
		for name := range option {
			if secret == "" {
				supported = false
				break
			}
			switch scheme := card.SecuritySchemes[name].(type) {
			case a2a.HTTPAuthSecurityScheme:
				if strings.EqualFold(scheme.Scheme, "bearer") {
					headers["Authorization"] = "Bearer " + secret
				} else if strings.EqualFold(scheme.Scheme, "basic") {
					headers["Authorization"] = "Basic " + secret
				} else {
					supported = false
				}
			case a2a.APIKeySecurityScheme:
				if scheme.Location != "header" || scheme.Name == "" {
					supported = false
				} else {
					headers[scheme.Name] = secret
				}
			case a2a.OAuth2SecurityScheme, a2a.OpenIDConnectSecurityScheme:
				headers["Authorization"] = "Bearer " + secret
			default:
				supported = false
			}
		}
		if supported {
			return headers, nil
		}
	}
	return nil, fmt.Errorf("没有匹配的认证方案或缺少 Agent 凭据；支持 Bearer、Basic、header API key 和预签发的 OAuth/OIDC token")
}
func (s *Server) a2aClient(ctx context.Context, a Agent, secret string) (*a2aclient.Client, func(), error) {
	card, e := sdkCard(a.Card)
	if e != nil {
		return nil, func() {}, e
	}
	headers, e := negotiateAuth(card, secret)
	if e != nil {
		return nil, func() {}, e
	}
	for _, iface := range card.SupportedInterfaces {
		if iface.ProtocolBinding != "GRPC" {
			if e = validateURL(iface.URL); e != nil {
				return nil, func() {}, e
			}
		}
	}
	httpConn := httpClient(s.allowPrivate)
	httpConn.Timeout = 0 // Every invocation has a durable deadline; SSE lives until its context is canceled.
	var connections []*grpc.ClientConn
	grpcFactory := func(legacy bool) a2aclient.TransportFactory {
		return a2aclient.TransportFactoryFn(func(ctx context.Context, card *a2a.AgentCard, iface *a2a.AgentInterface) (a2aclient.Transport, error) {
			raw := iface.URL
			if !strings.Contains(raw, "://") {
				raw = "https://" + raw
			}
			u, e := url.Parse(raw)
			if e != nil || u.Hostname() == "" || u.User != nil {
				return nil, fmt.Errorf("invalid gRPC interface URL")
			}
			var creds credentials.TransportCredentials
			if u.Scheme == "https" {
				creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
			} else if u.Scheme == "http" && s.allowPrivate {
				creds = insecure.NewCredentials()
			} else {
				return nil, fmt.Errorf("gRPC 需要 TLS")
			}
			port := u.Port()
			if port == "" {
				port = "443"
			}
			target := net.JoinHostPort(u.Hostname(), port)
			dialer := httpConn.Transport.(*http.Transport).DialContext
			conn, e := grpc.NewClient("passthrough:///"+target, grpc.WithTransportCredentials(creds), grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) { return dialer(ctx, "tcp", address) }), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2*1024*1024)))
			if e != nil {
				return nil, e
			}
			connections = append(connections, conn)
			if legacy {
				return grpcv0.NewGRPCTransport(conn), nil
			}
			return grpcv1.NewGRPCTransport(conn), nil
		})
	}
	c, e := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(httpConn), a2aclient.WithRESTTransport(httpConn), a2aclient.WithCompatTransport("0.3", "JSONRPC", a2av0.NewJSONRPCTransportFactory(a2av0.JSONRPCTransportConfig{Client: httpConn})), a2aclient.WithTransport("GRPC", grpcFactory(false)), a2aclient.WithCompatTransport("0.3", "GRPC", grpcFactory(true)), a2aclient.WithConfig(a2aclient.Config{Polling: true}), a2aclient.WithCallInterceptors(agentAuthInterceptor{headers: headers}))
	cleanup := func() {
		httpConn.CloseIdleConnections()
		for _, conn := range connections {
			conn.Close()
		}
	}
	if e != nil {
		cleanup()
	}
	return c, cleanup, e
}
func (s *Server) callA2A(ctx context.Context, a Agent, input, secret string) (string, *int, bool, error) {
	client, close, e := s.a2aClient(ctx, a, secret)
	if e != nil {
		return "", nil, false, e
	}
	defer close()
	checkpoint, _ := ctx.Value(remoteCheckpointKey{}).(*remoteCheckpoint)
	var result a2a.SendMessageResult
	if checkpoint != nil && checkpoint.Previous != nil {
		result, e = client.GetTask(ctx, &a2a.GetTaskRequest{ID: checkpoint.Previous.ID})
	} else {
		msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(input))
		if key, ok := ctx.Value(invocationKey{}).(string); ok {
			msg.ID = stableHex(key, 16)
		}
		result, e = client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	}
	if e != nil {
		return "", nil, false, fmt.Errorf("A2A 调用失败: %w", e)
	}
	if msg, ok := result.(*a2a.Message); ok {
		text, e := messageText(msg)
		return text, nil, false, e
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		return "", nil, false, fmt.Errorf("A2A 返回未知响应")
	}
	defer func() {
		if ctx.Err() != nil && (checkpoint == nil || checkpoint.CanCancel == nil || checkpoint.CanCancel()) {
			cancelCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
			defer done()
			_, _ = client.CancelTask(cancelCtx, &a2a.CancelTaskRequest{ID: task.ID})
		}
	}()

	for {
		if checkpoint != nil {
			if e = checkpoint.Save(&RemoteTask{task.ID, task.ContextID, task.Status.State, task.Artifacts}); e != nil {
				return "", nil, false, e
			}
		}
		if task.Status.State == a2a.TaskStateCompleted {
			parts := []*a2a.Part{}
			for _, artifact := range task.Artifacts {
				parts = append(parts, artifact.Parts...)
			}
			if len(parts) == 0 && task.Status.Message != nil {
				parts = task.Status.Message.Parts
			}
			text, e := messageText(&a2a.Message{Parts: parts})
			return text, nil, false, e
		}
		if task.Status.State.Terminal() {
			return "", nil, false, fmt.Errorf("远端任务 %s: %s", task.ID, task.Status.State)
		}
		if task.Status.State == a2a.TaskStateInputRequired || task.Status.State == a2a.TaskStateAuthRequired {
			return "", nil, false, fmt.Errorf("远端任务 %s 等待补充输入或认证，请在 A2A 会话中继续", task.ID)
		}
		select {
		case <-ctx.Done():

			return "", nil, false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		task, e = client.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID})
		if e != nil {
			return "", nil, false, e
		}
	}
}
func (g *Gateway) serveRemoteA2A(w http.ResponseWriter, r *http.Request, p Principal, s *Server, agentID, transport string) {
	if p.Role == "viewer" {
		replyError(w, forbidden())
		return
	}
	st, e := s.store.snapshotE()
	if e != nil {
		replyError(w, e)
		return
	}
	a, ok := st.Agents[agentID]
	if !ok || a.Kind != "external" || a.Card == nil || a.Card.Protocol == "http-json" {
		replyError(w, missing())
		return
	}
	secret := ""
	if st.Secrets[a.ID] != "" {
		secret, e = s.store.decrypt(st.Secrets[a.ID])
		if e != nil {
			replyError(w, e)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	client, close, e := s.a2aClient(ctx, a, secret)
	if e != nil {
		replyError(w, bad(e.Error()))
		return
	}
	defer close()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	if transport == "rpc" {
		a2asrv.NewJSONRPCHandler(client).ServeHTTP(w, r)
	} else if transport == "rpc-0.3" {
		a2av0.NewJSONRPCHandler(client).ServeHTTP(w, r)
	} else {
		replyError(w, missing())
	}
}
