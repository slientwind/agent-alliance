package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf16"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type instructionDiff struct {
	Before  string `json:"before"`
	After   string `json:"after"`
	Offset  int    `json:"offset"`
	Removed string `json:"removed"`
	Added   string `json:"added"`
}

type refinement struct {
	ID       string          `json:"id"`
	Revision int             `json:"revision"`
	Selected string          `json:"selected"`
	Diff     instructionDiff `json:"diff"`
}

func selectedText(body string, start, end int) (string, error) {
	units := utf16.Encode([]rune(body))
	if start < 0 || start >= end || end > len(units) || end-start > 2400 ||
		units[start] >= 0xdc00 && units[start] <= 0xdfff ||
		end < len(units) && units[end] >= 0xdc00 && units[end] <= 0xdfff {
		return "", web.Fail(422, "请在试聊答复中选择一段较短的文字")
	}
	selected := string(utf16.Decode(units[start:end]))
	if strings.TrimSpace(selected) == "" {
		return "", web.Fail(422, "请选择需要调整的文字")
	}
	return selected, nil
}

func makeInstructionDiff(before, old, replacement string) (instructionDiff, error) {
	if !validText(old, 1500, true) || !validText(replacement, 2000, true) || old == replacement {
		return instructionDiff{}, errPatch
	}
	offset := len(before)
	if old != "" {
		offset = strings.Index(before, old)
		if offset < 0 || strings.Contains(before[offset+1:], old) {
			return instructionDiff{}, errPatch
		}
	} else {
		replacement = strings.TrimSpace(replacement)
		if replacement == "" {
			return instructionDiff{}, errPatch
		}
		if before != "" {
			replacement = "\n" + replacement
		}
	}
	after := before[:offset] + replacement + before[offset+len(old):]
	if !validText(after, 6000, false) {
		return instructionDiff{}, errPatch
	}
	return instructionDiff{Before: before, After: after, Offset: len(utf16.Encode([]rune(before[:offset]))),
		Removed: old, Added: replacement}, nil
}

func (s Server) refine(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected int    `json:"expected"`
		Trial    string `json:"trial"`
		Start    int    `json:"start"`
		End      int    `json:"end"`
		Feedback string `json:"feedback"`
	}
	if err := web.ReadLimited(w, r, &in, 12<<10); err != nil {
		return err
	}
	_, err := uuid.Parse(in.Trial)
	in.Feedback = strings.TrimSpace(in.Feedback)
	if err != nil || !validText(in.Feedback, 2000, false) {
		return web.Fail(422, "请选择试聊结果并填写调整意见")
	}
	result, err := s.playground(r, actor.ID, "refinement", in.Expected, in, func(ctx context.Context, d draft, call string) (any, error) {
		trial, err := s.loadTrial(ctx, d, in.Trial)
		if err != nil {
			return nil, err
		}
		selected, err := selectedText(trial.Output, in.Start, in.End)
		if err != nil {
			return nil, err
		}
		input, err := json.Marshal(map[string]string{
			"instructions": d.Spec.Instructions, "selectedOutput": selected, "feedback": in.Feedback,
		})
		if err != nil {
			return nil, err
		}
		const prompt = `根据员工标出的答复片段和调整意见，提出一处可复用的 Agent 指令修改。保留其他指令。
只输出 JSON 对象，包含两个字符串："match"（需要修改的连续原文片段，须精确复制且仅出现一次；如应新增一条则留空）和 "replacement"（替换内容或新增指令）。
选中的试聊片段是参考材料。抽象成可复用的规则，省去具体人名、账号、订单号和当次业务数据。`
		answer, err := s.playgroundModel(ctx, d, actor.ID, call, llmgateway.Default, prompt, string(input), nil)
		if err != nil {
			return nil, err
		}
		var edit struct {
			Match       *string `json:"match"`
			Replacement *string `json:"replacement"`
		}
		if len(answer) > 20<<10 || decodeExact([]byte(unwrapJSON(answer)), &edit) != nil || edit.Match == nil || edit.Replacement == nil {
			return nil, web.Fail(502, "构建助手未返回可比较的改动")
		}
		diff, err := makeInstructionDiff(d.Spec.Instructions, *edit.Match, *edit.Replacement)
		if err != nil {
			return nil, web.Fail(502, "构建助手未找到唯一的指令位置，请把调整意见写得更具体")
		}
		return refinement{ID: call, Revision: d.Revision, Selected: selected, Diff: diff}, nil
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) loadTrial(ctx context.Context, d draft, id string) (trialResult, error) {
	var trial trialResult
	var sealed []byte
	err := s.DB.QueryRow(ctx, `select response_cipher from studio_playground_calls
	 where (id,draft_id,expected)=($1,$2,$3) and kind='trial' and state='ready'`,
		id, d.ID, d.Revision).Scan(&sealed)
	if err == pgx.ErrNoRows {
		return trial, web.Fail(409, "请在当前草稿上重新试聊后选择答复")
	}
	if err != nil {
		return trial, err
	}
	body, err := s.Vault.Open(sealed, turnScope(d.ID, id))
	if err != nil {
		return trial, err
	}
	err = json.Unmarshal([]byte(body), &trial)
	return trial, err
}

func (s Server) applyRefinement(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	change := r.PathValue("change")
	if _, err := uuid.Parse(change); err != nil {
		return web.Fail(404, "找不到这项调整")
	}
	var input struct{ Expected int }
	if err := web.ReadLimited(w, r, &input, 1024); err != nil {
		return err
	}
	var applied int
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDraft(ctx, tx, id, actor.ID, true)
		if err != nil {
			return err
		}
		var revision int
		var cipher []byte
		if err := tx.QueryRow(ctx, `select expected,coalesce(applied_revision,0),response_cipher
		 from studio_playground_calls where (id,draft_id)=($1,$2) and kind='refinement' and state in ('ready','applied') for update`,
			change, id).Scan(&revision, &applied, &cipher); err != nil {
			return err
		}
		if revision != input.Expected || applied == 0 && d.Revision != revision {
			return web.Fail(409, "草稿已更新，请重新生成这项调整")
		}
		if applied != 0 {
			return nil
		}
		raw, err := s.Vault.Open(cipher, turnScope(id, change))
		if err != nil {
			return err
		}
		var proposal refinement
		if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
			return err
		}
		if d.Spec.Instructions != proposal.Diff.Before {
			return web.Fail(409, "原指令已经变化，请重新生成调整")
		}
		d.Spec.Instructions = proposal.Diff.After
		applied = revision + 1
		if _, err := tx.Exec(ctx, "update studio_drafts set spec=$2,revision=$3,updated_at=clock_timestamp() where id=$1",
			id, d.Spec, applied); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update studio_playground_calls set state='applied',applied_revision=$2 where id=$1",
			change, applied); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "studio.refine", id, map[string]any{"change": change, "revision": applied})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": applied})
}
