package evalsuite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

func validID(text string) bool {
	id, err := uuid.Parse(text)
	return err == nil && id != uuid.Nil && id.String() == text
}

func validText(text string, max int) bool {
	return strings.TrimSpace(text) != "" && len(text) <= max && utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	const root = "/api/spaces/{space}/eval-suites"
	mux.Handle("GET "+root, auth.Protect(false, s.list))
	mux.Handle("POST "+root, auth.Protect(false, s.create))
	mux.Handle("POST "+root+"/{suite}/cases", auth.Protect(false, s.capture))
	mux.Handle("GET "+root+"/{suite}/cases", auth.Protect(false, s.cases))
	mux.Handle("POST "+root+"/{suite}/runs", auth.Protect(false, s.start))
	mux.Handle("GET "+root+"/{suite}/runs", auth.Protect(false, s.runs))
}

const evalRole = `space_role(s.space_id,$2)='space_admin'`

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到团队空间")
	}
	return s.jsonRows(w, r, `select coalesce(jsonb_agg(to_jsonb(x) order by x.name,x.id),'[]'::jsonb) from (
	 select s.id,s.name,count(c.id) as "caseCount" from eval_suites s left join eval_cases c on c.suite_id=s.id
	 where s.space_id=$1 and `+evalRole+` group by s.id,s.name order by s.name,s.id limit 60)x`, space, actor.ID)
}

func (s Server) jsonRows(w http.ResponseWriter, r *http.Request, query string, args ...any) error {
	var out json.RawMessage
	if err := s.DB.QueryRow(r.Context(), query, args...).Scan(&out); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, out)
}

func (s Server) create(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	var input struct{ Name string }
	if err := web.ReadLimited(w, r, &input, 4096); err != nil {
		return err
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validID(space) || !validText(input.Name, 360) || utf8.RuneCountInString(input.Name) > 120 {
		return web.Fail(422, "请填写团队体检集名称")
	}
	var id string
	err := s.DB.QueryRow(r.Context(), `insert into eval_suites(space_id,name,maintainer)
	 select s.id,$3,$2 from spaces s where s.id=$1 and s.kind='team' and space_role(s.id,$2)='space_admin' returning id`, space, actor.ID, input.Name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的团队空间")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id})
}

type captureSpec struct {
	Artifact string     `json:"artifactId"`
	Version  int        `json:"version"`
	Checks   Assertions `json:"assertions,omitempty"`
}

func (s Server) capture(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, suite := r.PathValue("space"), r.PathValue("suite")
	var input captureSpec
	if err := web.ReadLimited(w, r, &input, 24<<10); err != nil {
		return err
	}
	if !validID(space) || !validID(suite) || !validID(input.Artifact) || input.Version < 1 {
		return web.Fail(422, "请选择已完成的团队成果版本")
	}
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var goal, answer, run string
		var quotes []byte
		err := tx.QueryRow(ctx, `select plan.brief,v.body,r.id,kb_run_citations(r.id,$2) from eval_suites s
		 join projects p on p.space_id=s.space_id join artifacts a on a.project_id=p.id
		 join artifact_versions v on v.artifact_id=a.id join runs r on r.id=a.run_id
		 join plans plan on (plan.project_id,plan.version)=(p.id,r.plan_version)
		 join project_members pm on (pm.project_id,pm.member_id)=(p.id,$2)
		 where (s.id,s.space_id,a.id,v.version)=($1,$3,$4,$5) and `+evalRole+`
		  and r.status='completed' and (pm.can_review or p.owner=$2) and distribution_readable(a.id,v.version,$2)
		  and not exists(select from business_query_provenance q where q.run_id=r.id and (
		   q.restricted_to_actor or q.provenance->>'sensitivity' in ('secret','confidential')))
		  and not exists(select from general_kb_evidence e join kb_documents d on d.id=e.document_id
		   where e.run_id=r.id and d.sensitivity in ('secret','confidential'))
		  for update of s`, suite, actor.ID, space, input.Artifact, input.Version).Scan(&goal, &answer, &run, &quotes)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select id from eval_cases where (suite_id,artifact_id,artifact_version)=($1,$2,$3)",
			suite, input.Artifact, input.Version).Scan(&id); err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, "select count(*) from eval_cases where suite_id=$1", suite).Scan(&count); err != nil {
			return err
		}
		if count >= 50 {
			return web.Fail(409, "体检集已有 50 条用例")
		}
		var refs []struct {
			Ref string
			knowledge.KnowledgeCitationKey
		}
		if err := json.Unmarshal(quotes, &refs); err != nil {
			return err
		}
		citations := make([]string, 0, min(12, len(refs)))
		keys := make([]knowledge.KnowledgeCitationKey, 0, min(12, len(refs)))
		for _, ref := range refs[:min(12, len(refs))] {
			citations = append(citations, ref.Ref)
			keys = append(keys, ref.KnowledgeCitationKey)
		}
		fixture, err := knowledge.ResolveKnowledgeEvidence(ctx, tx, space, actor.ID, keys)
		if err != nil {
			return err
		}
		fixture, err = sanitizeFixture(ctx, tx, s.Vault, fixture)
		if err != nil {
			return err
		}
		goal, err = policy.AnonymizeKnowledgeQuestion(ctx, tx, s.Vault, goal)
		if err != nil || goal == "" {
			return web.Fail(422, "无法从成果提取可复用的脱敏任务")
		}
		checks := input.Checks
		if len(checks.Facts)+len(checks.Citations)+len(checks.Numbers) == 0 {
			// Capture numeric facts from non-sensitive approved answer lines.
			lines := strings.SplitN(answer, "\n", 81)
			lines = lines[:min(80, len(lines))]
			fields := make(map[string]any, len(lines))
			for _, line := range lines {
				if len(line) <= 2000 {
					fields[line] = line
				}
			}
			if err := policy.MaskSensitiveFields(ctx, tx, s.Vault, fields); err != nil {
				return err
			}
			for index, line := range lines {
				if clean, ok := fields[line].(string); ok && clean == line {
					continue
				}
				lines[index] = ""
			}
			checks = capturedAssertions(strings.Join(lines, "\n"), citations)
		}
		for i, fact := range checks.Facts {
			clean, err := policy.AnonymizeKnowledgeQuestion(ctx, tx, s.Vault, fact)
			if err != nil {
				return err
			}
			checks.Facts[i] = clean
		}
		for i, number := range checks.Numbers {
			clean, err := policy.AnonymizeKnowledgeQuestion(ctx, tx, s.Vault, number.Key)
			if err != nil {
				return err
			}
			// Short labels such as 数量 can be used after the field-level rule check.
			if clean == "" {
				switch key := strings.TrimSpace(number.Key); key {
				case "数量", "金额", "总额", "单价", "税率", "毛利", "时长", "天数", "订单数", "退货率", "净额", "合计":
					clean = key
				}
			}
			fields := map[string]any{number.Key: number.Value, "key": clean}
			if err := policy.MaskSensitiveFields(ctx, tx, s.Vault, fields); err != nil {
				return err
			}
			if fields[number.Key] == "[已脱敏]" || fields["key"] == "[已脱敏]" {
				return web.Fail(422, "指标断言涉及受保护数据")
			}
			checks.Numbers[i].Key = clean
		}
		if !validateAssertions(checks) {
			return web.Fail(422, "请补充可核验的关键事实、引用或数值断言")
		}
		for _, ref := range checks.Citations {
			found := false
			for _, allowed := range citations {
				found = found || ref == allowed
			}
			if !found {
				return web.Fail(422, "引用须来自已选成果的授权知识")
			}
		}
		return tx.QueryRow(ctx, `insert into eval_cases(suite_id,artifact_id,artifact_version,input_prompt,assertions,captured_by,fixture)
		 values($1,$2,$3,$4,$5,$6,$7) returning id`, suite, input.Artifact, input.Version, goal, checks, actor.ID, fixture).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到有权沉淀的团队成果")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id})
}

const caseReadable = `eval_case_readable(c.artifact_id,c.artifact_version,$2)`

func sanitizeFixture(ctx context.Context, tx pgx.Tx, vault resources.Vault, hits []knowledge.KnowledgeEvidence) ([]knowledge.KnowledgeEvidence, error) {
	fields := make(map[string]any)
	for _, hit := range hits {
		if hit.Sensitivity == "public" || hit.Sensitivity == "internal" {
			fields[hit.CitationKey().Label()] = hit.Excerpt
		}
	}
	if err := policy.MaskSensitiveFields(ctx, tx, vault, fields); err != nil {
		return nil, err
	}
	out := make([]knowledge.KnowledgeEvidence, 0, len(hits))
	for _, hit := range hits {
		if value, found := fields[hit.CitationKey().Label()]; found && value != "[已脱敏]" {
			out = append(out, knowledge.KnowledgeEvidence{Document: hit.Document, Space: hit.Space, Version: hit.Version,
				Ordinal: hit.Ordinal, Excerpt: value.(string), Sensitivity: hit.Sensitivity})
		}
	}
	return out, nil
}

func (s Server) cases(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, suite := r.PathValue("space"), r.PathValue("suite")
	if !validID(space) || !validID(suite) {
		return web.Fail(404, "找不到团队体检集")
	}
	return s.jsonRows(w, r, `select coalesce(jsonb_agg(jsonb_build_object('id',c.id,'prompt',c.input_prompt,'assertions',c.assertions)
	 order by c.created_at,c.id),'[]'::jsonb) from eval_cases c join eval_suites s on s.id=c.suite_id
	 where (s.id,s.space_id)=($1,$3) and `+evalRole+` and `+caseReadable, suite, actor.ID, space)
}

func (s Server) start(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, suite := r.PathValue("space"), r.PathValue("suite")
	var in struct {
		Kind           string `json:"kind"`
		Tier           string `json:"baselineTier"`
		CandidateModel string `json:"candidateModelId,omitempty"`
	}
	if err := web.ReadLimited(w, r, &in, 4096); err != nil {
		return err
	}
	if in.Tier == "" {
		in.Tier = "default"
	}
	if !validID(space) || !validID(suite) || (in.Tier != "default" && in.Tier != "fast" && in.Tier != "reasoning") ||
		(in.Kind != "model" && in.Kind != "knowledge") || (in.Kind == "model" && !validID(in.CandidateModel)) ||
		(in.CandidateModel != "" && !validID(in.CandidateModel)) {
		return web.Fail(422, "请选择有效的评测空间、模型与预检类型")
	}
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into eval_runs(suite_id,actor,baseline_tier,candidate_model_id,kind)
		 select s.id,$2,$4,nullif($5,'')::uuid,$6 from eval_suites s where (s.id,s.space_id)=($1,$3) and `+evalRole+`
		 and ($5='' or exists(select from model_catalog m join model_providers p on p.id=m.provider_id and p.enabled
		  where m.id=nullif($5,'')::uuid and m.enabled and 'text'=any(m.capabilities)
		  and exists(select from model_pricing q where q.model_id=m.id and q.effective_at<=statement_timestamp())))
		 returning id`, suite, actor.ID, space, in.Tier, in.CandidateModel, in.Kind).Scan(&id)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into eval_run_cases(run_id,case_id)
		 select $1,c.id from eval_cases c where c.suite_id=$3 and `+caseReadable, id, actor.ID, suite)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "体检集没有当前可读取的用例")
		}
		return database.Audit(ctx, tx, actor.ID, "eval.run.requested", id, map[string]any{"suite": suite, "caseCount": tag.RowsAffected()})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可评测的体检集或模型")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]string{"id": id})
}

func (s Server) runs(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, suite := r.PathValue("space"), r.PathValue("suite")
	if !validID(space) || !validID(suite) {
		return web.Fail(404, "找不到团队体检集")
	}
	return s.jsonRows(w, r, `select coalesce(jsonb_agg(jsonb_build_object('id',v.id,'kind',v.kind,'state',v.state,'report',v.diff_report_json,
	 'candidateModelId',v.candidate_model_id,'createdAt',v.created_at) order by v.created_at desc,v.id),'[]'::jsonb) from (
	  select r.* from eval_runs r join eval_suites s on s.id=r.suite_id where (s.id,s.space_id)=($1,$3) and `+evalRole+`
	  order by r.created_at desc,r.id limit 40)v`, suite, actor.ID, space)
}

func citationLabels(matches []knowledge.KnowledgeEvidence) []string {
	out := make([]string, 0, len(matches))
	for _, hit := range matches {
		out = append(out, hit.CitationKey().Label())
	}
	return out
}
