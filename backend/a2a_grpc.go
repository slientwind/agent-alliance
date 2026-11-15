package main

import (
	"context"
	"iter"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/a2aproject/a2a-go-v1/a2a"
	grpcv0 "github.com/a2aproject/a2a-go-v1/a2agrpc/v0"
	grpcv1 "github.com/a2aproject/a2a-go-v1/a2agrpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type grpcA2ARouter struct{ gateway *Gateway }
type grpcTargetKey struct{}

func (g *Gateway) grpcIdentity(ctx context.Context) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	r, _ := http.NewRequestWithContext(ctx, "POST", "http://grpc/api/v1/a2a", nil)
	for _, k := range []string{"authorization", "x-workspace-id"} {
		if values := md.Get(k); len(values) == 1 {
			r.Header.Set(k, values[0])
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "workspace bearer credential required")
	}
	p, e := g.authenticate(r)
	if e != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid workspace credential")
	}
	ctx = context.WithValue(ctx, principalKey{}, p)
	if values := md.Get("x-alliance-target"); len(values) == 1 {
		ctx = context.WithValue(ctx, grpcTargetKey{}, values[0])
	}
	return ctx, nil
}
func (h *grpcA2ARouter) resolve(ctx context.Context, tenant string) (*a2aEndpoint, error) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	if !ok {
		return nil, a2a.ErrUnauthenticated
	}
	target, _ := ctx.Value(grpcTargetKey{}).(string)
	if target == "" {
		target = tenant
	}
	parts := strings.Split(target, "/")
	if len(parts) != 2 || (parts[0] != "agents" && parts[0] != "services") {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "provide tenant or x-alliance-target as agents/<id> or services/<id>")
	}
	s, e := h.gateway.server(p.TenantID)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	base := strings.TrimRight(os.Getenv("ALLIANCE_PUBLIC_URL"), "/") + "/api/v1/a2a/" + target
	endpoint := &a2aEndpoint{s, parts[0], parts[1], base, p}
	if _, e = endpoint.card(ctx); e != nil {
		return nil, e
	}
	return endpoint, nil
}

type authenticatedGRPCStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s authenticatedGRPCStream) Context() context.Context { return s.ctx }
func (g *Gateway) newGRPCServer(options ...grpc.ServerOption) *grpc.Server {
	options = append(options, grpc.MaxRecvMsgSize(1024*1024), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, e := g.grpcIdentity(ctx)
		if e != nil {
			return nil, e
		}
		return next(ctx, req)
	}), grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		ctx, e := g.grpcIdentity(stream.Context())
		if e != nil {
			return e
		}
		return next(srv, authenticatedGRPCStream{stream, ctx})
	}))
	server := grpc.NewServer(options...)
	router := &grpcA2ARouter{g}
	grpcv1.NewHandler(protocolHandler(router, "1.0")).RegisterWith(server)
	grpcv0.NewHandler(protocolHandler(router, "0.3")).RegisterWith(server)
	return server
}
func (g *Gateway) startGRPC() (func(), error) {
	addr := os.Getenv("ALLIANCE_GRPC_ADDR")
	if addr == "" {
		return func() {}, nil
	}
	var options []grpc.ServerOption
	cert, key := os.Getenv("ALLIANCE_TLS_CERT"), os.Getenv("ALLIANCE_TLS_KEY")
	if cert != "" && key != "" {
		creds, e := credentials.NewServerTLSFromFile(cert, key)
		if e != nil {
			return nil, e
		}
		options = append(options, grpc.Creds(creds))
	} else if os.Getenv("ALLIANCE_INSECURE_GRPC") != "true" {
		return nil, fmtError("configure TLS or explicit ALLIANCE_INSECURE_GRPC for local use")
	}
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		return nil, e
	}
	server := g.newGRPCServer(options...)
	go server.Serve(listener)
	return server.Stop, nil
}
func fmtError(message string) error { return status.Error(codes.FailedPrecondition, message) }

func (h *grpcA2ARouter) GetTask(ctx context.Context, q *a2a.GetTaskRequest) (*a2a.Task, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.GetTask(ctx, q)
}

func (h *grpcA2ARouter) ListTasks(ctx context.Context, q *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.ListTasks(ctx, q)
}

func (h *grpcA2ARouter) CancelTask(ctx context.Context, q *a2a.CancelTaskRequest) (*a2a.Task, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.CancelTask(ctx, q)
}

func (h *grpcA2ARouter) SendMessage(ctx context.Context, q *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.SendMessage(ctx, q)
}

func (h *grpcA2ARouter) GetTaskPushConfig(ctx context.Context, q *a2a.GetTaskPushConfigRequest) (*a2a.TaskPushConfig, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.GetTaskPushConfig(ctx, q)
}

func (h *grpcA2ARouter) ListTaskPushConfigs(ctx context.Context, q *a2a.ListTaskPushConfigRequest) ([]*a2a.TaskPushConfig, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.ListTaskPushConfigs(ctx, q)
}

func (h *grpcA2ARouter) CreateTaskPushConfig(ctx context.Context, q *a2a.CreateTaskPushConfigRequest) (*a2a.TaskPushConfig, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.CreateTaskPushConfig(ctx, q)
}

func (h *grpcA2ARouter) GetExtendedAgentCard(ctx context.Context, q *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return nil, e
	}
	return endpoint.GetExtendedAgentCard(ctx, q)
}
func (h *grpcA2ARouter) DeleteTaskPushConfig(ctx context.Context, q *a2a.DeleteTaskPushConfigRequest) error {
	endpoint, e := h.resolve(ctx, q.Tenant)
	if e != nil {
		return e
	}
	return endpoint.DeleteTaskPushConfig(ctx, q)
}
func (h *grpcA2ARouter) SubscribeToTask(ctx context.Context, q *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		endpoint, e := h.resolve(ctx, q.Tenant)
		if e != nil {
			yield(nil, e)
			return
		}
		for event, e := range endpoint.SubscribeToTask(ctx, q) {
			if !yield(event, e) {
				return
			}
		}
	}
}
func (h *grpcA2ARouter) SendStreamingMessage(ctx context.Context, q *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		endpoint, e := h.resolve(ctx, q.Tenant)
		if e != nil {
			yield(nil, e)
			return
		}
		for event, e := range endpoint.SendStreamingMessage(ctx, q) {
			if !yield(event, e) {
				return
			}
		}
	}
}
