package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type apiError struct {
	code    int
	message string
}

func (e apiError) Error() string    { return e.message }
func bad(message string) error      { return apiError{400, message} }
func missing() error                { return apiError{404, "资源不存在"} }
func conflict(message string) error { return apiError{409, message} }

type Server struct {
	allowPrivate bool
	store        *Store
	token        string
	client       *http.Client
	cancelMu     sync.Mutex
	cancels      map[string]context.CancelFunc
	wg           sync.WaitGroup
	timeout      time.Duration
	logf         func(string, ...any)
}

func newServer(store *Store, token string, allowPrivate bool) *Server {
	timeout := 60 * time.Second
	if value, e := strconv.Atoi(os.Getenv("ALLIANCE_RUN_TIMEOUT_SECONDS")); e == nil && value >= 5 && value <= 3600 {
		timeout = time.Duration(value) * time.Second
	}
	return &Server{allowPrivate: allowPrivate, store: store, token: token, client: httpClient(allowPrivate), cancels: map[string]context.CancelFunc{}, timeout: timeout, logf: log.Printf}
}
func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return bad("JSON 无效、字段未知或请求过大")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return bad("请求必须包含单个 JSON 对象")
	}
	return nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" {
		jsonReply(w, 200, map[string]string{"status": "ok"})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		jsonReply(w, 404, map[string]string{"error": "资源不存在"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
		jsonReply(w, 401, map[string]string{"error": "需要有效的 API key"})
		return
	}
	code, out, err := s.route(w, r)
	if err != nil {
		code = 500
		var ae apiError
		if errors.As(err, &ae) {
			code = ae.code
		}
		jsonReply(w, code, map[string]string{"error": err.Error()})
		return
	}
	jsonReply(w, code, out)
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) (int, any, error) {
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/"), "/")
	kind := p[0]
	resourceID := ""
	action := ""
	if len(p) > 1 {
		resourceID = p[1]
	}
	if len(p) > 2 {
		action = p[2]
	}
	if len(p) > 3 {
		return 0, nil, missing()
	}
	method := r.Method
	if kind == "integrations" && resourceID == "langfuse" {
		return s.langfuseRoute(w, r, action)
	}
	if kind == "openapi.json" && method == "GET" {
		return 200, openAPI(), nil
	}
	if kind == "metrics" && method == "GET" {
		st, readErr := s.store.snapshotE()
		if readErr != nil {
			return 0, nil, apiError{503, "工作区存储暂不可用"}
		}
		total, success, active, completed := len(st.Runs), 0, 0, 0
		var duration int64
		for _, run := range st.Runs {
			if run.Status == "succeeded" {
				success++
			}
			if !terminal(run.Status) {
				active++
			} else {
				completed++
				duration += run.DurationMS
			}
		}
		rate := float64(0)
		if completed > 0 {
			rate = float64(success) * 100 / float64(completed)
		}
		avg := int64(0)
		if completed > 0 {
			avg = duration / int64(completed)
		}
		return 200, map[string]any{"totalRuns": total, "successRate": rate, "activeRuns": active, "averageDurationMs": avg, "agents": len(st.Agents), "services": len(st.Services)}, nil
	}
	if kind == "agents" && resourceID == "generate" && method == "POST" {
		var q struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			ProviderID  string `json:"providerId"`
			AgentID     string `json:"agentId"`
			APIKey      string `json:"apiKey"`
		}
		if e := decode(w, r, &q); e != nil {
			return 0, nil, e
		}
		if !nonempty(q.Name) || !nonempty(q.Description) {
			return 0, nil, bad("名称和职责描述必填")
		}
		st, readErr := s.store.snapshotE()
		if readErr != nil {
			return 0, nil, apiError{503, "工作区存储暂不可用"}
		}
		provider, ok := st.Providers[q.ProviderID]
		if !ok {
			return 0, nil, bad("请选择模型配置")
		}
		prompt := "你是" + q.Name + "。\n职责：" + q.Description + "\n请依据用户输入完成任务；清晰区分事实与推断。信息不足时说明缺口，不编造结果。"
		if provider.Type != "mock" {
			a := Agent{Name: "Agent 构建器", Kind: "managed", ProviderID: q.ProviderID, Prompt: "根据用户描述编写一个可直接使用的中文 Agent system prompt。只输出 prompt，明确任务、输入、输出和约束，不生成代码。"}

			if q.AgentID != "" {
				if _, ok := st.Agents[q.AgentID]; !ok {
					return 0, nil, missing()
				}
				a.ID = q.AgentID
			}
			if q.APIKey != "" {
				a.ID = id("draft_")
				encrypted, e := s.store.encrypt(q.APIKey)
				if e != nil {
					return 0, nil, e
				}
				st.Secrets[a.ID] = encrypted
			}
			out, _, _, e := s.call(r.Context(), a, q.Name+"\n"+q.Description, st)
			if e != nil {
				return 0, nil, apiError{502, e.Error()}
			}
			prompt = out
		}
		return 200, map[string]any{"prompt": prompt, "mock": provider.Type == "mock"}, nil
	}
	if action == "invoke" && method == "POST" && (kind == "agents" || kind == "services") {
		return s.invoke(w, r, kind, resourceID)
	}
	if kind == "runs" {
		if action != "" && action != "cancel" {
			return 0, nil, missing()
		}
		if method == "GET" && action != "" {
			return 0, nil, missing()
		}
		if method == "GET" {
			st, readErr := s.store.snapshotE()
			if readErr != nil {
				return 0, nil, apiError{503, "工作区存储暂不可用"}
			}
			if resourceID != "" {
				run, ok := st.Runs[resourceID]
				if !ok {
					return 0, nil, missing()
				}
				return 200, run, nil
			}
			list := []Run{}
			for _, run := range st.Runs {
				if status := r.URL.Query().Get("status"); status != "" && status != run.Status {
					continue
				}
				list = append(list, run)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].StartedAt.After(list[j].StartedAt) })
			return 200, list, nil
		}
		if method == "POST" && action == "cancel" {
			if s.store.db != nil {
				e := s.cancelDurable(r.Context(), resourceID)
				return 200, map[string]bool{"cancelled": true}, e
			}
			s.cancelMu.Lock()
			defer s.cancelMu.Unlock()
			err := s.store.updateContext(r.Context(), func(st *State) error {
				run, ok := st.Runs[resourceID]
				if !ok {
					return missing()
				}
				if terminal(run.Status) {
					return conflict("运行已结束")
				}
				t := now()
				run.Status = "cancelled"
				run.Error = "用户取消运行"
				run.EndedAt = &t
				run.DurationMS = t.Sub(run.StartedAt).Milliseconds()
				st.Runs[resourceID] = run
				return nil
			})
			if err != nil {
				return 0, nil, err
			}
			if cancel := s.cancels[resourceID]; cancel != nil {
				cancel()
			}
			return 200, map[string]bool{"cancelled": true}, nil
		}
	}
	if kind == "services" && action == "publish" && (method == "POST" || method == "DELETE") {
		var out Service
		err := s.store.updateContext(r.Context(), func(st *State) error {
			v, ok := st.Services[resourceID]
			if !ok {
				return missing()
			}
			if method == "DELETE" {
				v.Published = nil
			} else {
				if e := validNodes(v.Nodes, *st); e != nil {
					return e
				}
				agents := map[string]Agent{}
				for _, n := range v.Nodes {
					agents[n.AgentID] = st.Agents[n.AgentID]
				}
				v.ReleaseVersion++
				v.Published = &Release{Version: v.ReleaseVersion, Nodes: v.Nodes, Agents: agents, PublishedAt: now()}
			}
			st.Services[resourceID] = v
			out = v
			return nil
		})
		return 200, out, err
	}
	if action != "" {
		return 0, nil, missing()
	}
	if method == "GET" {
		st, readErr := s.store.snapshotE()
		if readErr != nil {
			return 0, nil, apiError{503, "工作区存储暂不可用"}
		}
		switch kind {
		case "providers":
			if resourceID != "" {
				v, ok := st.Providers[resourceID]
				if !ok {
					return 0, nil, missing()
				}
				return 200, v, nil
			}
			list := []Provider{}
			for _, v := range st.Providers {
				list = append(list, v)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			return 200, list, nil
		case "agents":
			if resourceID != "" {
				v, ok := st.Agents[resourceID]
				if !ok {
					return 0, nil, missing()
				}
				return 200, v, nil
			}
			list := []Agent{}
			for _, v := range st.Agents {
				list = append(list, v)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
			return 200, list, nil
		case "services":
			if resourceID != "" {
				v, ok := st.Services[resourceID]
				if !ok {
					return 0, nil, missing()
				}
				return 200, v, nil
			}
			list := []Service{}
			for _, v := range st.Services {
				list = append(list, v)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			return 200, list, nil
		}
		return 0, nil, missing()
	}
	if method == "DELETE" && resourceID != "" {
		err := s.store.updateContext(r.Context(), func(st *State) error {
			switch kind {
			case "agents":
				if _, ok := st.Agents[resourceID]; !ok {
					return missing()
				}
				for _, v := range st.Services {
					for _, n := range v.Nodes {
						if n.AgentID == resourceID {
							return conflict("Agent 被服务草稿引用，请先移除节点")
						}
					}
					if v.Published != nil {
						if _, ok := v.Published.Agents[resourceID]; ok {
							return conflict("Agent 被已发布服务引用，请先撤销发布")
						}
					}
				}
				delete(st.Agents, resourceID)
				delete(st.Secrets, resourceID)
			case "providers":
				if _, ok := st.Providers[resourceID]; !ok {
					return missing()
				}
				for _, a := range st.Agents {
					if a.ProviderID == resourceID {
						return conflict("模型配置仍被 Agent 引用")
					}
				}
				for _, v := range st.Services {
					if v.Published != nil {
						for _, a := range v.Published.Agents {
							if a.ProviderID == resourceID {
								return conflict("模型配置仍被发布版本引用")
							}
						}
					}
				}
				delete(st.Providers, resourceID)
				delete(st.Secrets, resourceID)
			case "services":
				if _, ok := st.Services[resourceID]; !ok {
					return missing()
				}
				delete(st.Services, resourceID)
			default:
				return missing()
			}
			return nil
		})
		return 200, map[string]bool{"deleted": true}, err
	}
	if method != "POST" && method != "PUT" {
		return 0, nil, apiError{405, "不支持此方法"}
	}
	if method == "POST" && resourceID != "" || method == "PUT" && resourceID == "" {
		return 0, nil, missing()
	}
	code := 200
	if method == "POST" {
		code = 201
	}
	switch kind {
	case "providers":
		var v Provider
		if e := decode(w, r, &v); e != nil {
			return 0, nil, e
		}
		err := s.store.updateContext(r.Context(), func(st *State) error {
			if method == "PUT" {
				if _, ok := st.Providers[resourceID]; !ok {
					return missing()
				}
				v.ID = resourceID
			} else {
				v.ID = id("mdl_")
			}
			if !nonempty(v.Name) || !nonempty(v.Model) {
				return bad("名称和模型名称必填")
			}
			if v.Type != "mock" && v.Type != "openai" {
				return bad("不支持的模型类型")
			}
			if v.Type == "openai" {
				if e := validateURL(v.BaseURL); e != nil {
					return bad(e.Error())
				}
			}
			if v.Key != "" {
				return bad("模型密钥必须配置到具体 Agent，不能保存在共享模型配置中")
			}
			v.HasKey = false
			delete(st.Secrets, v.ID)
			st.Providers[v.ID] = v
			return nil
		})
		return code, v, err
	case "agents":
		var v Agent
		if e := decode(w, r, &v); e != nil {
			return 0, nil, e
		}
		err := s.store.updateContext(r.Context(), func(st *State) error {
			if method == "PUT" {
				old, ok := st.Agents[resourceID]
				if !ok {
					return missing()
				}
				v.ID = resourceID
				v.Version = old.Version + 1
				v.CreatedAt = old.CreatedAt
			} else {
				v.ID = id("agt_")
				v.Version = 1
				v.CreatedAt = now()
			}
			if v.Status == "" {
				v.Status = "active"
			}
			if v.ClearKey {
				if v.Key != "" {
					return bad("不能同时清除和设置密钥")
				}
				delete(st.Secrets, v.ID)
			}
			if e := validAgent(&v, *st); e != nil {
				return e
			}
			if v.ClearKey {
				if v.Key != "" {
					return bad("不能同时清除和设置密钥")
				}
				delete(st.Secrets, v.ID)
			}
			v.ClearKey = false
			if v.Key != "" {
				secret, e := s.store.encrypt(v.Key)
				if e != nil {
					return e
				}
				st.Secrets[v.ID] = secret
			}
			v.Key = ""
			v.HasKey = st.Secrets[v.ID] != ""
			st.Agents[v.ID] = v
			return nil
		})
		return code, v, err
	case "services":
		var v Service
		if e := decode(w, r, &v); e != nil {
			return 0, nil, e
		}
		err := s.store.updateContext(r.Context(), func(st *State) error {
			if method == "PUT" {
				old, ok := st.Services[resourceID]
				if !ok {
					return missing()
				}
				v.ID = resourceID
				v.Version = old.Version + 1
				v.Published = old.Published
				v.ReleaseVersion = old.ReleaseVersion
			} else {
				v.ID = id("svc_")
				v.Version = 1
				v.Published = nil
				v.ReleaseVersion = 0
			}
			if !nonempty(v.Name) {
				return bad("服务名称必填")
			}
			if e := validNodes(v.Nodes, *st); e != nil {
				return e
			}
			st.Services[v.ID] = v
			return nil
		})
		return code, v, err
	}
	return 0, nil, missing()
}
func validAgent(v *Agent, st State) error {
	if v.Status != "active" && v.Status != "disabled" {
		return bad("无效状态")
	}
	if v.Kind == "external" {
		if v.Card == nil {
			return bad("需要 Agent Card")
		}
		c := v.Card
		if !nonempty(c.Name) || !nonempty(c.Description) || !nonempty(c.Version) || len(c.Skills) == 0 {
			return bad("Agent Card 缺少 name、description、version 或 skills")
		}
		for _, skill := range c.Skills {
			if !nonempty(skill.ID) || !nonempty(skill.Name) {
				return bad("skill 缺少 id/name")
			}
		}
		if c.Protocol == "" {
			if len(c.Extra["supportedInterfaces"]) > 0 || c.ProtocolVersion != "" {
				if len(c.Extra["supportedInterfaces"]) > 0 {
					c.Protocol = "a2a"
				} else {
					c.Protocol = "a2a-jsonrpc"
				}
			}
		}
		if c.Protocol == "http-json" {
			if e := validateURL(c.URL); e != nil {
				return bad(e.Error())
			}
		} else if c.Protocol == "a2a" || c.Protocol == "a2a-jsonrpc" {
			parsed, e := sdkCard(c)
			if e != nil {
				return bad("无效 A2A Card: " + e.Error())
			}
			for _, iface := range parsed.SupportedInterfaces {
				if iface.ProtocolBinding != "JSONRPC" && iface.ProtocolBinding != "HTTP+JSON" && iface.ProtocolBinding != "GRPC" {
					return bad("不支持的 A2A transport")
				}
				if iface.ProtocolVersion != "0.3" && iface.ProtocolVersion != "0.3.0" && iface.ProtocolVersion != "1.0" && iface.ProtocolVersion != "1.0.0" {
					return bad("支持 A2A 0.3 和 1.0")
				}
				if iface.ProtocolBinding != "GRPC" {
					if e := validateURL(iface.URL); e != nil {
						return bad(e.Error())
					}
				}
			}
			key := v.Key
			if key == "" && st.Secrets[v.ID] != "" {
				key = "configured"
			}
			if _, e := negotiateAuth(parsed, key); e != nil {
				return bad(e.Error())
			}
		} else {
			return bad("支持 http-json、a2a 或 a2a-jsonrpc")
		}

		if v.Name == "" {
			v.Name = c.Name
		}
		if v.Description == "" {
			v.Description = c.Description
		}
		v.ProviderID = ""
		v.Prompt = ""
	} else if v.Kind == "managed" {
		if _, ok := st.Providers[v.ProviderID]; !ok {
			return bad("模型配置不存在")
		}
		if !nonempty(v.Prompt) {
			return bad("system prompt 必填")
		}
		v.Card = nil
	} else {
		return bad("无效 Agent 类型")
	}
	if !nonempty(v.Name) || !nonempty(v.Description) {
		return bad("名称和描述必填")
	}
	return nil
}
func validNodes(nodes []Node, st State) error {
	if _, e := layers(nodes); e != nil {
		return bad(e.Error())
	}
	for _, n := range nodes {
		a, ok := st.Agents[n.AgentID]
		if !ok {
			return bad("编排引用不存在的 Agent")
		}
		if a.Status != "active" {
			return conflict("编排引用已停用 Agent")
		}
	}
	return nil
}
func (s *Server) invoke(w http.ResponseWriter, r *http.Request, kind, resourceID string) (int, any, error) {
	var q struct {
		Input string `json:"input"`
	}
	if e := decode(w, r, &q); e != nil {
		return 0, nil, e
	}
	if !nonempty(q.Input) || len(q.Input) > 64000 {
		return 0, nil, bad("输入必填且不得超过 64KB")
	}
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if len(s.cancels) >= 32 {
		return 0, nil, apiError{429, "运行并发已达上限"}
	}
	st, readErr := s.store.snapshotE()
	if readErr != nil {
		return 0, nil, apiError{503, "工作区存储暂不可用"}
	}
	nodes := []Node{}
	agents := map[string]Agent{}
	run := Run{ID: id("run_"), TraceID: id("trace_"), TargetID: resourceID, Status: "queued", Input: q.Input, StartedAt: now(), Spans: []Span{}}
	if kind == "agents" {
		a, ok := st.Agents[resourceID]
		if !ok {
			return 0, nil, missing()
		}
		if a.Status != "active" {
			return 0, nil, conflict("Agent 已停用")
		}
		agents[a.ID] = a
		nodes = append(nodes, Node{ID: "agent", AgentID: a.ID})
		run.TargetType = "agent"
		run.TargetName = a.Name
		run.Version = a.Version
	} else {
		v, ok := st.Services[resourceID]
		if !ok {
			return 0, nil, missing()
		}
		if v.Published == nil {
			return 0, nil, conflict("服务尚未发布")
		}
		for id := range v.Published.Agents {
			if a, ok := st.Agents[id]; !ok || a.Status != "active" {
				return 0, nil, conflict("发布版本引用的 Agent 已停用或删除")
			}
		}
		nodes = v.Published.Nodes
		agents = v.Published.Agents
		run.TargetType = "service"
		run.TargetName = v.Name
		run.Version = v.Published.Version
	}
	if s.store.db != nil {
		queued, e := s.enqueue(r, run, nodes, agents, st)
		return 202, queued, e
	}
	if e := s.store.updateContext(r.Context(), func(st *State) error { st.Runs[run.ID] = run; return nil }); e != nil {
		return 0, nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	s.cancels[run.ID] = cancel
	s.wg.Add(1)
	go func() { defer cancel(); s.execute(ctx, run, nodes, agents, st) }()
	return 202, run, nil
}
func (s *Server) shutdown() {
	s.cancelMu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.cancelMu.Unlock()
	s.wg.Wait()
}
