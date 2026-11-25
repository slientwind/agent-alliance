package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type trialResult struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Output   string `json:"output"`
}

func (s Server) try(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected int    `json:"expected"`
		Message  string `json:"message"`
	}
	if err := web.ReadLimited(w, r, &in, 24<<10); err != nil {
		return err
	}
	in.Message = strings.TrimSpace(in.Message)
	if !validText(in.Message, 3500, false) {
		return web.Fail(422, "请填写试聊消息")
	}
	stream := &trialStream{w: w}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		ctx := r.Context()
		stream.allow = func() bool { return ctx.Err() == nil }
	}
	var emit func(string)
	if stream.allow != nil {
		emit = func(text string) { stream.send("chunk", map[string]string{"text": text}) }
	}
	result, err := s.playground(r, actor.ID, "trial", in.Expected, in, func(ctx context.Context, snapshot draft, call string) (any, error) {
		if !validText(snapshot.Spec.Instructions, 6000, false) && len(snapshot.Spec.Steps) == 0 {
			return nil, web.Fail(409, "请先和构建助手生成任务步骤或说明")
		}
		spec, err := json.Marshal(snapshot.Spec)
		if err != nil {
			return nil, err
		}
		answer, err := s.playgroundModel(ctx, snapshot, actor.ID, call, snapshot.Spec.Tier,
			"按照下面的 Agent 草稿完成这条试聊。\n"+string(spec), in.Message, emit)
		if err != nil {
			return nil, err
		}
		if !validText(answer, 12000, false) {
			return nil, web.Fail(502, "试聊没有返回可显示的答复")
		}
		return trialResult{ID: call, Revision: snapshot.Revision, Output: answer}, nil
	})
	if err != nil {
		if stream.started {
			var problem web.Problem
			message := "试聊没有完成，请重试。"
			if errors.As(err, &problem) {
				message = problem.Message
			}
			stream.send("error", map[string]string{"error": message})
			return nil
		}
		return err
	}
	if stream.allow != nil {
		stream.send("complete", result)
		return nil
	}
	return web.JSON(w, http.StatusOK, result)
}

type trialStream struct {
	w       http.ResponseWriter
	allow   func() bool
	started bool
}

func (stream *trialStream) send(event string, payload any) {
	if !stream.allow() {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if !stream.started {
		stream.w.Header().Set("Content-Type", "text/event-stream")
		stream.w.Header().Set("Cache-Control", "no-store")
		stream.w.Header().Set("X-Accel-Buffering", "no")
		stream.started = true
	}
	if _, err := fmt.Fprintf(stream.w, "event: %s\ndata: %s\n\n", event, data); err == nil {
		_ = http.NewResponseController(stream.w).Flush()
	}
}

func (s Server) playgroundModel(ctx context.Context, d draft, actor, call string, tier llmgateway.Tier, rules, task string, emit func(string)) (string, error) {
	sub, stop := context.WithTimeout(ctx, 65*time.Second)
	defer stop()
	result, err := s.Gateway.Call(sub, llmgateway.CallIdentity{OperationKey: turnScope(d.ID, call),
		TeamID: d.Team, MemberID: actor}, llmgateway.Request{Tier: tier, MaxTokens: 2800, EmitText: emit, Messages: []resources.Message{
		{Role: "system", Content: rules}, {Role: "user", Content: task},
	}})
	if errors.Is(err, llmgateway.ErrBudgetExceeded) {
		return "", web.Fail(409, "模型额度不足，请核对团队和个人额度")
	}
	if err != nil || len(result.Reply.ToolCalls) != 0 || result.Reply.FinishReason != "" && result.Reply.FinishReason != "stop" {
		return "", web.Fail(502, "模型尚未完成答复，请稍后重试")
	}
	return strings.TrimSpace(result.Reply.Text), nil
}

func (s Server) playground(r *http.Request, actor, kind string, expected int, input any,
	work func(context.Context, draft, string) (any, error)) (any, error) {
	id, err := studioID(r)
	if err != nil {
		return nil, err
	}
	key := r.Header.Get("Idempotency-Key")
	if expected < 1 || key == "" || len(key) > 180 {
		return nil, web.Fail(422, "请核对当前草稿后重试")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	ctx, call := r.Context(), uuid.NewString()
	var snapshot draft
	var replay []byte
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		snapshot, err = getDraft(ctx, tx, id, actor, true)
		if err != nil {
			return err
		}
		hash := web.Hash(string(payload))
		var prior, state string
		var stored []byte
		err := tx.QueryRow(ctx, `select id,request_hash,state,response_cipher from studio_playground_calls
		 where (draft_id,kind,request_key)=($1,$2,$3)`, id, kind, key).Scan(&prior, &stored, &state, &replay)
		if err == nil {
			if !bytes.Equal(stored, hash) {
				return web.Fail(409, "这个提交编号已用于另一条消息")
			}
			if state != "ready" && state != "applied" {
				return web.Fail(409, "这条消息已提交，请等待答复或重新发起")
			}
			call = prior
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if snapshot.Revision != expected {
			return web.Fail(409, "草稿已更新，请刷新后继续")
		}
		_, err = tx.Exec(ctx, `insert into studio_playground_calls(id,draft_id,kind,expected,request_key,request_hash)
		 values($1,$2,$3,$4,$5,$6)`, call, id, kind, expected, key, hash)
		return err
	})
	if err != nil {
		return nil, err
	}
	if replay != nil {
		body, err := s.Vault.Open(replay, turnScope(id, call))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(body), nil
	}
	result, err := work(ctx, snapshot, call)
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		err = s.commitPlayground(commitCtx, id, actor, call, expected, result)
	}
	if err != nil {
		if _, finalErr := s.DB.Exec(commitCtx, "update studio_playground_calls set state='failed' where id=$1 and state='working'", call); finalErr != nil {
			return nil, finalErr
		}
		return nil, err
	}
	return result, nil
}

func (s Server) commitPlayground(ctx context.Context, draftID, actor, call string, expected int, result any) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	cipher, err := s.Vault.Seal(string(body), turnScope(draftID, call))
	if err != nil {
		return err
	}
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		current, err := getDraft(ctx, tx, draftID, actor, true)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return web.Fail(409, "草稿在试聊或微调期间已变化，请重新生成")
		}
		_, err = tx.Exec(ctx, `update studio_playground_calls set state='ready',response_cipher=$2
		 where id=$1 and state='working'`, call, cipher)
		return err
	})
}
