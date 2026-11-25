package resources

import (
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type RemotePolicy struct {
	ExecutionSeconds int `json:"executionSeconds"`
	QuerySeconds     int `json:"querySeconds"`
	WindowSeconds    int `json:"windowSeconds"`
	IntervalSeconds  int `json:"intervalSeconds"`
	ReadRetries      int `json:"readRetries"`
}

func defaultRemotePolicy() RemotePolicy { return RemotePolicy{60, 10, 900, 5, 2} }
func (p RemotePolicy) valid() bool {
	return p.ExecutionSeconds >= 1 && p.ExecutionSeconds <= 90 && p.QuerySeconds >= 1 && p.QuerySeconds <= 30 && p.WindowSeconds >= p.QuerySeconds && p.WindowSeconds <= 86400 && p.IntervalSeconds >= 1 && p.IntervalSeconds <= p.WindowSeconds && p.ReadRetries >= 0 && p.ReadRetries <= 2
}

type AgentCard struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Version     string       `json:"version"`
	Protocol    string       `json:"protocolVersion"`
	URL         string       `json:"url"`
	Transport   string       `json:"preferredTransport"`
	Adapter     *AdapterSpec `json:"allianceAdapter,omitempty"`
	Interfaces  []struct {
		URL       string `json:"url"`
		Transport string `json:"transport"`
	} `json:"additionalInterfaces,omitempty"`
	Skills []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"skills"`
}

func (c Catalog) PermittedAgentEndpoint(ctx context.Context, raw string) bool {
	return permittedAgentEndpoint(ctx, egress.Policy{DB: c.DB}, raw)
}

func permittedAgentEndpoint(ctx context.Context, policy egress.Policy, raw string) bool {
	return Endpoint(raw) && policy.Allowed(ctx, raw, false)
}

type externalDraft struct {
	ID, Team       string
	Expected       int
	Endpoint       string
	Card           json.RawMessage
	Key            string
	KeepCredential bool
	Policy         RemotePolicy
}

func (c Catalog) external(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in externalDraft
	in.Policy = defaultRemotePolicy()
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if !in.Policy.valid() {
		return web.Fail(422, "请核对执行、单次查询、核对窗口和重试参数")
	}
	ctx := r.Context()
	card, endpoint, err := c.parseCard(ctx, in.Card, in.Endpoint)
	if err != nil {
		return err
	}
	if in.Key != "" && in.Endpoint == "" && (endpoint != card.URL || (card.Transport != "" && card.Transport != "JSONRPC")) {
		return web.Fail(422, "请明确选择接收此凭据的备用 JSONRPC 地址")
	}
	return c.saveExternal(w, r, u, in, card, endpoint)
}

func (c Catalog) saveExternal(w http.ResponseWriter, r *http.Request, u identity.Member, in externalDraft, card AgentCard, endpoint string) error {
	ctx := r.Context()
	id := in.ID
	rev := 0
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if id == "" {
			var err error
			id, err = createTeamAgent(ctx, tx, u.ID, in.Team)
			if err != nil {
				return err
			}
		} else {
			if err := lockTeamAgent(ctx, tx, u.ID, id, in.Team); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "select max(revision) from agent_revisions where agent_id=$1", id).Scan(&rev); err != nil {
				return err
			}
		}
		if in.Expected != rev {
			return web.Fail(409, "Agent 修订已变更")
		}
		rev++
		var encrypted []byte
		if in.Key != "" {
			var err error
			encrypted, err = c.Vault.Seal(in.Key, fmt.Sprintf("agent/%s/%d", id, rev))
			if err != nil {
				return err
			}
		}
		if in.KeepCredential {
			if rev <= 1 || in.Key != "" {
				return web.Fail(422, "请明确选择沿用或替换凭据")
			}
			var oldKey []byte
			var oldURL, oldProtocol, oldTemplate string
			var oldWebhook []byte
			if err := tx.QueryRow(ctx, `select key_override,rpc_endpoint,coalesce(card->>'protocolVersion',''),
				coalesce(card#>>'{allianceAdapter,template}',''),card#>'{allianceAdapter,webhook}'
				from agent_revisions where agent_id=$1 and revision=$2 and card is not null`,
				id, rev-1).Scan(&oldKey, &oldURL, &oldProtocol, &oldTemplate, &oldWebhook); err != nil {
				return err
			}
			if len(oldKey) > 0 {
				if oldURL != endpoint || oldProtocol != card.Protocol || (card.Adapter != nil &&
					(oldTemplate != card.Adapter.Template || !card.Adapter.sameWebhookRoute(oldWebhook))) {
					return web.Fail(422, "端点变化，请重新提供凭据或明确选择无需凭据")
				}
				key, err := c.Vault.Open(oldKey, fmt.Sprintf("agent/%s/%d", id, rev-1))
				if err != nil {
					return err
				}
				encrypted, err = c.Vault.Seal(key, fmt.Sprintf("agent/%s/%d", id, rev))
				if err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, "insert into agent_revisions(agent_id,revision,name,instructions,card,key_override,created_by,remote_policy,rpc_endpoint) values($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, rev, card.Name, card.Description, in.Card, encrypted, u.ID, in.Policy, endpoint); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.card", id, map[string]int{"revision": rev})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "revision": rev})
}

type part struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type a2aResult struct {
	Kind      string
	ID        string
	ContextID string
	Parts     []part
	Status    struct {
		State   string
		Message struct {
			MessageID string
			Parts     []part
		}
	}
	Artifacts []struct{ Parts []part }
}
type RemoteWait struct {
	Task, Context, State string
	Question             string
	QuestionMessage      string
	NoText               bool
}

func (e *RemoteWait) Error() string { return "外部任务状态：" + e.State }

func (m Model) SupplementRemote(ctx context.Context, receipt RemoteWait, messageID, input string) (Reply, error) {
	if !m.External || m.Adapter != nil || receipt.Task == "" || receipt.Context == "" || messageID == "" || web.Blank(input) {
		return Reply{}, errors.New("需要原远端任务、上下文及本次已登记的补件")
	}
	if m.RemoteOperations != nil && !m.RemoteOperations.CanSupplement() {
		return Reply{}, web.Fail(409, "此发布修订的远端补件证据已失效")
	}
	sub, stop := context.WithTimeout(ctx, time.Duration(m.Policy.ExecutionSeconds)*time.Second)
	defer stop()
	return m.remote(sub, "message/send", &receipt, map[string]any{
		"message": map[string]any{"kind": "message", "messageId": messageID, "role": "user",
			"taskId": receipt.Task, "contextId": receipt.Context, "parts": []part{{Kind: "text", Text: input}}},
		"configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}},
	})
}

func (m Model) CheckRemote(ctx context.Context, receipt RemoteWait, cancel bool) (Reply, error) {
	if !m.External || receipt.Task == "" || (m.Adapter != nil && m.Adapter.Protocol != AdapterWebhook) {
		return Reply{}, errors.New("需要已登记的外部任务编号")
	}
	if !m.RemoteOperations.CanFollow(cancel) {
		return Reply{}, web.Fail(409, "此发布修订没有有效的远端查询或取消证据")
	}
	method := "tasks/get"
	params := map[string]any{"id": receipt.Task, "historyLength": 0}
	if cancel {
		method = "tasks/cancel"
		delete(params, "historyLength")
	}
	sub, stop := context.WithTimeout(ctx, time.Duration(m.Policy.QuerySeconds)*time.Second)
	defer stop()
	if m.Adapter != nil {
		return m.webhookFollow(sub, receipt, cancel)
	}
	return m.remote(sub, method, &receipt, params)
}
func (m Model) remote(ctx context.Context, method string, expected *RemoteWait, params any) (Reply, error) {
	if !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("外部主机未获准")
	}
	rpcID := web.Token()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID, "method": method, "params": params})
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	m.ExternalTrace.inject(req)
	if m.Key != "" {
		req.Header.Set("Authorization", "Bearer "+m.Key)
	}
	rsp, err := m.externalResponse(req)
	if err != nil {
		return Reply{}, err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != 200 {
		return Reply{}, fmt.Errorf("外部服务 HTTP %d", rsp.StatusCode)
	}
	var envelope struct {
		JSONRPC, ID string
		Result      a2aResult
		Error       json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(rsp.Body, 4<<20)).Decode(&envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != rpcID || len(envelope.Error) != 0 {
		return Reply{}, errors.New("无法确定外部调用结果")
	}
	result := envelope.Result
	if expected != nil && (result.Kind != "task" || result.ID != expected.Task || (expected.Context != "" && result.ContextID != expected.Context)) {
		return Reply{}, errors.New("远端回执与原任务不符")
	}
	var receipt *RemoteWait
	parts := result.Parts
	if result.Kind == "task" {
		if result.ID == "" || len(result.ID) > 512 || len(result.ContextID) > 512 {
			return Reply{}, errors.New("外部任务缺少标识")
		}
		receipt = &RemoteWait{Task: result.ID, Context: result.ContextID, State: result.Status.State}
		if receipt.State == "input-required" {
			receipt.Question = remoteQuestion(result.Status.Message.Parts)
			message := result.Status.Message.MessageID
			if len(message) <= 256 && !strings.ContainsRune(message, 0) {
				receipt.QuestionMessage = message
			}
		}
		switch receipt.State {
		case "submitted", "working", "input-required", "auth-required", "canceled", "failed", "rejected", "unknown":
			return Reply{}, receipt
		case "completed":
		default:
			return Reply{}, errors.New("外部任务状态无效")
		}
		for _, a := range result.Artifacts {
			parts = append(parts, a.Parts...)
		}
		if len(parts) == 0 {
			parts = result.Status.Message.Parts
		}
	} else if result.Kind != "message" {
		return Reply{}, errors.New("不支持的外部响应类型")
	}
	var text []string
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			text = append(text, p.Text)
		}
	}
	if len(text) == 0 {
		if receipt != nil {
			receipt.NoText = true
			return Reply{}, receipt
		}
		return Reply{}, errors.New("返回成果不包含已声明支持的文本操作")
	}
	return Reply{Text: strings.Join(text, "\n"), Receipt: receipt}, nil
}

func remoteQuestion(parts []part) string {
	const limit = 1000
	var question []rune
	for _, p := range parts {
		if p.Kind != "text" || web.Blank(p.Text) {
			continue
		}
		if len(question) != 0 {
			question = append(question, '\n')
		}
		for _, ch := range strings.TrimSpace(p.Text) {
			if !unicode.IsControl(ch) || ch == '\n' || ch == '\t' {
				question = append(question, ch)
			}
			if len(question) >= limit {
				return strings.TrimSpace(string(question[:limit]))
			}
		}
	}
	return strings.TrimSpace(string(question))
}

// Registered cards are public service descriptions. The separately sealed credential
// is deliberately outside this document and cannot be returned by this endpoint.
func (c Catalog) cardRevision(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	revision, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || revision < 1 {
		return web.Fail(422, "Agent 修订无效")
	}
	var card json.RawMessage
	err = c.DB.QueryRow(r.Context(), `select ar.card from agent_revisions ar join agents a on a.id=ar.agent_id join team_members tm on tm.team_id=a.team_id where ar.agent_id=$1 and ar.revision=$2 and tm.member_id=$3 and ar.card is not null and not a.removed`, r.PathValue("id"), revision, u.ID).Scan(&card)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, card)
}

func (c Catalog) parseCard(ctx context.Context, raw json.RawMessage, selected string) (AgentCard, string, error) {
	var card AgentCard
	if json.Unmarshal(raw, &card) != nil || card.Adapter != nil || card.Protocol != "0.3.0" || card.Name == "" || card.Version == "" || card.URL == "" || len(card.Skills) == 0 {
		return card, "", web.Fail(422, "需要包含版本、主端点和技能的 A2A 0.3.0 Agent Card")
	}
	for _, skill := range card.Skills {
		if skill.ID == "" || skill.Name == "" {
			return card, "", web.Fail(422, "Card 技能缺少 ID 或名称")
		}
	}
	allowed := func(address string) bool {
		return (selected == "" || address == selected) && c.PermittedAgentEndpoint(ctx, address)
	}
	if (card.Transport == "" || card.Transport == "JSONRPC") && allowed(card.URL) {
		return card, card.URL, nil
	}
	for _, entry := range card.Interfaces {
		if entry.Transport == "JSONRPC" && allowed(entry.URL) {
			return card, entry.URL, nil
		}
	}
	return card, "", web.Fail(422, "请选择 Card 已声明且企业准许的 HTTPS JSONRPC 接口")
}

func (c Catalog) discoverCard(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ URL string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	address := strings.TrimSpace(in.URL)
	if !Endpoint(address) {
		return web.Fail(422, "需要企业已准许的 HTTPS Card 地址")
	}
	parsed, _ := url.Parse(address)
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/.well-known/agent-card.json"
	}
	ctx, stop := context.WithTimeout(r.Context(), 10*time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, "GET", parsed.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	(ExternalTrace{ActorID: u.ID}).inject(req)
	base, err := providerClient()
	if err != nil {
		return err
	}
	client, err := (egress.Policy{DB: c.DB}).Client(ctx, parsed.String(), false, base)
	if err != nil {
		return web.Fail(422, "需要企业已准许的 HTTPS Card 地址")
	}
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return web.Fail(502, "无法读取公开 Card，请检查地址和连通性")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return web.Fail(422, "公开 Card 未返回有效内容")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return web.Fail(422, "Card 无法读取或超过 256 KiB")
	}
	if _, _, err = c.parseCard(ctx, raw, ""); err != nil {
		return err
	}
	return web.JSON(w, 200, json.RawMessage(raw))
}
