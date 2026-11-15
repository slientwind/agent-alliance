package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func layers(nodes []Node) ([][]Node, error) {
	if len(nodes) == 0 || len(nodes) > 32 {
		return nil, fmt.Errorf("编排需要 1–32 个节点")
	}
	index := map[string]Node{}
	for _, n := range nodes {
		if !nonempty(n.ID) || !nonempty(n.AgentID) {
			return nil, fmt.Errorf("节点 ID 和 Agent 必填")
		}
		if _, ok := index[n.ID]; ok {
			return nil, fmt.Errorf("节点 ID 重复")
		}
		index[n.ID] = n
	}
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, dep := range n.DependsOn {
			if _, ok := index[dep]; !ok {
				return nil, fmt.Errorf("依赖节点不存在: %s", dep)
			}
			if seen[dep] {
				return nil, fmt.Errorf("重复依赖")
			}
			seen[dep] = true
		}
	}
	done := map[string]bool{}
	out := [][]Node{}
	for len(done) < len(nodes) {
		group := []Node{}
		for _, n := range nodes {
			if done[n.ID] {
				continue
			}
			ready := true
			for _, d := range n.DependsOn {
				if !done[d] {
					ready = false
				}
			}
			if ready {
				group = append(group, n)
			}
		}
		if len(group) == 0 {
			return nil, fmt.Errorf("编排存在循环依赖")
		}
		out = append(out, group)
		for _, n := range group {
			done[n.ID] = true
		}
	}
	return out, nil
}
func (s *Server) remote(ctx context.Context, url, key string, body any) (map[string]any, error) {
	if err := validateURL(url); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key, ok := ctx.Value(invocationKey{}).(string); ok {
		req.Header.Set("Idempotency-Key", key)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var networkError net.Error
		return nil, upstreamNetworkError{timedOut: errors.As(err, &networkError) && networkError.Timeout()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamHTTPError{Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("读取远端响应失败")
	}
	if len(data) > 2*1024*1024 {
		return nil, fmt.Errorf("远端响应过大")
	}
	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("远端返回无效 JSON")
	}
	return out, nil
}
func (s *Server) call(ctx context.Context, a Agent, input string, st State) (string, *int, bool, error) {
	if a.Kind == "external" {
		c := a.Card
		if c == nil {
			return "", nil, false, fmt.Errorf("缺少 Agent Card")
		}
		key := ""
		if secret := st.Secrets[a.ID]; secret != "" {
			var err error
			key, err = s.store.decrypt(secret)
			if err != nil {
				return "", nil, false, fmt.Errorf("无法解密外部凭据")
			}
		}
		if c.Protocol == "http-json" {
			r, e := s.remote(ctx, c.URL, key, map[string]any{"input": input})
			if e != nil {
				return "", nil, false, e
			}
			o, ok := r["output"].(string)
			if !ok {
				return "", nil, false, fmt.Errorf("远端缺少字符串 output")
			}
			return o, nil, false, nil
		}
		return s.callA2A(ctx, a, input, key)
	}
	p, ok := st.Providers[a.ProviderID]
	if !ok {
		return "", nil, false, fmt.Errorf("模型配置不存在")
	}
	if p.Type == "mock" {
		delay := 180 * time.Millisecond
		if strings.Contains(input, "[slow]") {
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return "", nil, true, ctx.Err()
		case <-time.After(delay):
		}
		if strings.Contains(input, "[fail]") {
			return "", nil, true, fmt.Errorf("Mock 模型：已注入失败")
		}
		out := fmt.Sprintf("【%s · Mock】\n已完成：%s\n处理结果：依据职责「%s」完成信息整理，建议继续核验关键事实。", a.Name, truncate(input, 300), truncate(a.Prompt, 100))
		tokens := (len([]rune(input)) + len([]rune(out))) / 2
		return out, &tokens, true, nil
	}
	key := ""
	if secret := st.Secrets[a.ID]; secret != "" {
		var e error
		key, e = s.store.decrypt(secret)
		if e != nil {
			return "", nil, false, fmt.Errorf("无法解密模型密钥")
		}
	}
	r, e := s.remote(ctx, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", key, map[string]any{"model": p.Model, "messages": []any{map[string]string{"role": "system", "content": a.Prompt}, map[string]string{"role": "user", "content": input}}, "stream": false})
	if e != nil {
		return "", nil, false, e
	}
	choices, ok := r["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", nil, false, fmt.Errorf("模型未返回 choices")
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	out, ok := msg["content"].(string)
	if !ok {
		return "", nil, false, fmt.Errorf("模型返回内容不是文本")
	}
	var tokens *int
	if usage, ok := r["usage"].(map[string]any); ok {
		if n, ok := usage["total_tokens"].(float64); ok {
			v := int(n)
			tokens = &v
		}
	}
	return out, tokens, false, nil
}
func (s *Server) execute(ctx context.Context, run Run, nodes []Node, agents map[string]Agent, st State) {
	defer s.wg.Done()
	defer func() { s.cancelMu.Lock(); delete(s.cancels, run.ID); s.cancelMu.Unlock() }()
	if err := s.store.update(func(st *State) error {
		r := st.Runs[run.ID]
		if terminal(r.Status) {
			return fmt.Errorf("run 已终结")
		}
		r.Status = "running"
		st.Runs[r.ID] = r
		return nil
	}); err != nil {
		return
	}
	groups, err := layers(nodes)
	results := map[string]string{}
	var resultMu sync.Mutex
	for _, group := range groups {
		if err != nil || ctx.Err() != nil {
			break
		}
		var wg sync.WaitGroup
		var firstErr error
		for _, n := range group {
			wg.Add(1)
			go func(n Node) {
				defer wg.Done()
				a := agents[n.AgentID]
				input := run.Input
				if len(n.DependsOn) > 0 {
					items := []string{}
					resultMu.Lock()
					for _, d := range n.DependsOn {
						items = append(items, d+": "+results[d])
					}
					resultMu.Unlock()
					input = strings.Join(items, "\n\n")
				}
				if n.Instruction != "" {
					input = n.Instruction + "\n\n" + input
				}
				span := Span{ID: id("span_"), ParentID: run.TraceID, NodeID: n.ID, Name: a.Name, AgentVersion: a.Version, Status: "running", Input: input, StartedAt: now()}
				e := s.store.update(func(st *State) error {
					r := st.Runs[run.ID]
					if terminal(r.Status) {
						return context.Canceled
					}
					r.Spans = append(r.Spans, span)
					st.Runs[r.ID] = r
					return nil
				})
				if e == nil {
					span.Output, span.Tokens, span.Mock, e = s.call(ctx, a, input, st)
				}
				t := now()
				span.EndedAt = &t
				span.DurationMS = t.Sub(span.StartedAt).Milliseconds()
				span.Status = "succeeded"
				if e != nil {
					span.Status = "failed"
					span.Error = e.Error()
					if ctx.Err() == context.Canceled {
						span.Status = "cancelled"
					}
				}
				saveErr := s.store.update(func(st *State) error {
					r := st.Runs[run.ID]
					for i := range r.Spans {
						if r.Spans[i].ID == span.ID {
							r.Spans[i] = span
						}
					}
					st.Runs[r.ID] = r
					return nil
				})
				if e == nil {
					e = saveErr
				}
				resultMu.Lock()
				defer resultMu.Unlock()
				if e != nil && firstErr == nil {
					firstErr = e
				}
				results[n.ID] = span.Output
			}(n)
		}
		wg.Wait()
		err = firstErr
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	e := s.store.update(func(st *State) error {
		r := st.Runs[run.ID]
		if r.Status == "cancelled" {
			return nil
		}
		t := now()
		r.EndedAt = &t
		r.DurationMS = t.Sub(r.StartedAt).Milliseconds()
		r.Status = "succeeded"
		if err != nil {
			r.Status = "failed"
			r.Error = err.Error()
			if ctx.Err() == context.Canceled {
				r.Status = "cancelled"
			}
		}
		used := map[string]bool{}
		for _, n := range nodes {
			for _, d := range n.DependsOn {
				used[d] = true
			}
		}
		outputs := []string{}
		for _, n := range nodes {
			if !used[n.ID] && results[n.ID] != "" {
				outputs = append(outputs, results[n.ID])
			}
		}
		r.Output = strings.Join(outputs, "\n\n")
		st.Runs[r.ID] = r
		return nil
	})
	if e != nil {
		s.logf("persist run completion: %v", e)
	}
}
