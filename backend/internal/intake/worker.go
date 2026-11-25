package intake

import (
	"alliance/internal/database"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type intakeEvent struct {
	ID       string `json:"-"`
	Ordinal  int    `json:"event"`
	Text     string `json:"text"`
	Readable bool   `json:"-"`
}
type unresolved struct {
	Event  int    `json:"event"`
	Reason string `json:"reason"`
}
type extraction struct {
	Items      []candidate  `json:"items"`
	Unresolved []unresolved `json:"unresolved"`
}

func loadBatchEvents(ctx context.Context, tx pgx.Tx, batch, actor string) ([]intakeEvent, error) {
	rows, err := tx.Query(ctx, `select e.id,b.ordinal,e.body,intake_event_readable(e.id,$2)
	 from intake_batch_events b join intake_events e on e.id=b.event_id where b.batch_id=$1 order by b.ordinal`, batch, actor)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[intakeEvent])
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || slices.ContainsFunc(events, func(e intakeEvent) bool { return !e.Readable }) {
		return nil, web.Fail(409, "这批消息的资料访问权已变化")
	}
	return events, nil
}

var (
	keyPattern  = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_-]{2,63}$`)
	keyTokens   = regexp.MustCompile(`(?i)[a-z0-9][a-z0-9_-]*`)
	objectTypes = []string{"order", "shipment", "invoice", "ticket", "purchase", "document"}
	intents     = []string{"refund", "return", "replace", "delivery", "billing", "inquiry", "correction", "approval", "other"}
)

func validateCandidate(value candidate, events []intakeEvent) (candidate, error) {
	value.Type = strings.ToLower(strings.TrimSpace(value.Type))
	value.Key = strings.ToUpper(strings.TrimSpace(value.Key))
	value.Intent = strings.ToLower(strings.TrimSpace(value.Intent))
	value.Evidence = strings.TrimSpace(value.Evidence)
	value.Note = strings.TrimSpace(value.Note)
	if value.Event < 1 || value.Event > len(events) || !slices.Contains(objectTypes, value.Type) || !slices.Contains(intents, value.Intent) ||
		!keyPattern.MatchString(value.Key) || value.Evidence == "" || len(value.Evidence) > 1500 || len(value.Note) > 800 ||
		strings.ContainsRune(value.Note, 0) || !strings.Contains(events[value.Event-1].Text, value.Evidence) {
		return candidate{}, web.Fail(422, "请为事项选择原消息中可核对的对象编号、诉求和依据")
	}
	tokens := keyTokens.FindAllString(strings.ToUpper(value.Evidence), -1)
	if !slices.Contains(tokens, value.Key) {
		return candidate{}, web.Fail(422, "事项编号需要出现在选定依据中")
	}
	return value, nil
}

func parseExtraction(raw string, events []intakeEvent) (extraction, error) {
	var result extraction
	if len(raw) > 64<<10 || json.Unmarshal([]byte(strings.TrimSpace(raw)), &result) != nil || len(result.Items) > 80 || len(result.Unresolved) > 20 {
		return result, web.Fail(422, "整理结果格式不完整，请缩小批次后再试")
	}
	coverage, unassigned := map[int]bool{}, map[int]bool{}
	var unique []candidate
	seen := map[candidate]bool{}
	for _, item := range result.Items {
		item, err := validateCandidate(item, events)
		if err != nil {
			return extraction{}, err
		}
		coverage[item.Event] = true
		key := item
		key.Evidence, key.Note = "", ""
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, item)
	}
	for _, missing := range result.Unresolved {
		if missing.Event < 1 || missing.Event > len(events) || coverage[missing.Event] || unassigned[missing.Event] ||
			strings.TrimSpace(missing.Reason) == "" || len(missing.Reason) > 800 || strings.ContainsRune(missing.Reason, 0) {
			return extraction{}, web.Fail(422, "待核对的消息范围不完整")
		}
		unassigned[missing.Event] = true
	}
	if len(coverage)+len(unassigned) != len(events) {
		return extraction{}, web.Fail(422, "有消息缺少归并结果或待核对说明")
	}
	result.Items = unique
	return result, nil
}

type work struct {
	run, project, team, actor, status string
	batch, caseID                     *string
	expected                          *int
	instruction                       []byte
	pins                              []string
	usable                            bool
}

func loadWork(ctx context.Context, tx pgx.Tx, run string) (work, error) {
	job := work{run: run}
	err := tx.QueryRow(ctx, `select r.project_id,r.charge_team,r.actor,r.status,w.batch_id,w.case_id,w.base_revision,w.instruction_cipher,w.event_pins,
		general_run_inputs_usable(r.id) and not exists(select from unnest(w.event_pins) p where not intake_event_readable(p,r.actor))
		from general_intake_work w join runs r on r.id=w.run_id where r.id=$1 for update of r,w`, run).
		Scan(&job.project, &job.team, &job.actor, &job.status, &job.batch, &job.caseID, &job.expected, &job.instruction, &job.pins, &job.usable)
	return job, err
}

// Advance runs only message-intake work. The general worker continues to
// dispatch all workflows through its existing activity names.
func (s Server) Advance(ctx context.Context, run string) (bool, error) {
	var job work
	var events []intakeEvent
	var input string
	identified, started := false, false
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		job, err = loadWork(ctx, tx, run)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		identified = true
		if job.status != "queued" && job.status != "running" {
			return nil
		}
		if job.status == "running" {
			return finishWork(ctx, tx, job, "上次处理结果无法确认，请重新提交")
		}
		if !job.usable {
			return finishWork(ctx, tx, job, "当前任务或相关资料的访问权已变化")
		}
		if job.batch != nil {
			events, err = loadBatchEvents(ctx, tx, *job.batch, job.actor)
			if err != nil {
				return workInputError(ctx, tx, job, err)
			}
			_, err = tx.Exec(ctx, "update intake_batches set state='working' where id=$1", job.batch)
		} else {
			input, err = s.caseInput(ctx, tx, job)
		}
		if err != nil {
			return workInputError(ctx, tx, job, err)
		}
		_, err = tx.Exec(ctx, "update runs set status='running' where id=$1", run)
		started = err == nil
		return err
	})
	if err != nil || !started {
		return identified, err
	}
	var plan string
	if job.batch != nil {
		excerpts := slices.Clone(events)
		for i := range excerpts {
			excerpts[i].Text = web.TraceText(excerpts[i].Text)
		}
		encoded, err := json.Marshal(excerpts)
		if err != nil {
			return true, err
		}
		input = string(encoded)
		plan = `整理这些企业工作消息。每条消息可对应多个事项。对象类别只允许 order,shipment,invoice,ticket,purchase,document。
诉求只允许 refund,return,replace,delivery,billing,inquiry,correction,approval,other。
输出 JSON 对象 {"items":[{"event":1,"objectType":"order","objectKey":"ORD-1","intent":"refund","evidence":"原文中的完整短句，包含对象编号","note":"供人工核对的简述"}],"unresolved":[{"event":2,"reason":"没有明确的对象编号"}]}。
证据必须逐字来自该条消息。只归并有明确编号的对象，编号有歧义的消息写进 unresolved。每条消息必须覆盖。不要执行任何外部联系或业务操作。`
	} else {
		plan = "接着处理这一项企业业务。仅依据本次提供的文字和当前问题，返回供同事核对的处理意见。保留对象编号，缺少依据时请说明，勿执行实际业务操作。"
	}
	reply, callErr := s.Gateway.Call(ctx, llmgateway.CallIdentity{OperationKey: intakeScope(run), TeamID: job.team, MemberID: job.actor, RunID: run, Turn: 1},
		llmgateway.Request{Tier: llmgateway.Default, MaxTokens: 4096, Messages: []resources.Message{{Role: "system", Content: plan}, {Role: "user", Content: input}}})
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return true, database.WithTx(settle, s.DB, func(tx pgx.Tx) error {
		current, err := loadWork(settle, tx, run)
		if err != nil || current.status != "running" {
			return err
		}
		if !current.usable {
			return finishWork(settle, tx, current, "处理期间资料访问权已变化")
		}
		if callErr != nil || len(reply.Reply.ToolCalls) != 0 || strings.TrimSpace(reply.Reply.Text) == "" {
			return finishWork(settle, tx, current, "模型未完成整理，请缩小范围后重新提交")
		}
		if current.batch != nil {
			fresh, err := loadBatchEvents(settle, tx, *current.batch, current.actor)
			if err != nil {
				return workInputError(settle, tx, current, err)
			}
			result, err := parseExtraction(reply.Reply.Text, fresh)
			if err != nil {
				return workInputError(settle, tx, current, err)
			}
			if err := persistExtraction(settle, tx, *current.batch, result); err != nil {
				return err
			}
		} else if err := s.saveCaseResult(settle, tx, current, reply.Reply.Text); err != nil {
			return workInputError(settle, tx, current, err)
		}
		return finishWork(settle, tx, current, "")
	})
}

func workInputError(ctx context.Context, tx pgx.Tx, job work, err error) error {
	var problem web.Problem
	if errors.As(err, &problem) {
		return finishWork(ctx, tx, job, problem.Message)
	}
	return err
}

func finishWork(ctx context.Context, tx pgx.Tx, job work, note string) error {
	state, batchState := "completed", "review"
	if note != "" {
		state, batchState = "failed", "failed"
	}
	if _, err := tx.Exec(ctx, "update runs set status=$2,finished_at=clock_timestamp() where id=$1", job.run, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update general_intake_work set failure=nullif($2,'') where run_id=$1", job.run, note); err != nil {
		return err
	}
	if job.batch != nil {
		if _, err := tx.Exec(ctx, `update intake_batches set state=$2,revision=revision+1,failure=nullif($3,'') where id=$1`,
			job.batch, batchState, note); err != nil {
			return err
		}
	}
	return database.Audit(ctx, tx, job.actor, "intake.run."+state, job.run, map[string]any{})
}

func persistExtraction(ctx context.Context, tx pgx.Tx, batch string, result extraction) error {
	for i, item := range result.Items {
		if _, err := tx.Exec(ctx, `insert into intake_candidates(batch_id,event_ordinal,position,object_type,object_key,intent,evidence,note)
		 values($1,$2,$3,$4,$5,$6,$7,$8)`, batch, item.Event, i+1, item.Type, item.Key, item.Intent, item.Evidence, item.Note); err != nil {
			return err
		}
	}
	for _, item := range result.Unresolved {
		if _, err := tx.Exec(ctx, "insert into intake_unresolved values($1,$2,$3)", batch, item.Event, item.Reason); err != nil {
			return err
		}
	}
	return nil
}

func (s Server) caseInput(ctx context.Context, tx pgx.Tx, job work) (string, error) {
	var key, kind, intent string
	var revision int
	var excerpts []string
	err := tx.QueryRow(ctx, `select c.object_key,c.object_type,c.intent,c.revision,
	 array(select e.evidence from intake_case_events e where e.case_id=c.id and e.event_id=any($3::uuid[])
	  and intake_event_readable(e.event_id,$2) order by e.event_id)
	 from intake_cases c where c.id=$1 and c.project_id=$4`, job.caseID, job.actor, job.pins, job.project).
		Scan(&key, &kind, &intent, &revision, &excerpts)
	if err != nil {
		return "", err
	}
	if revision != *job.expected || len(excerpts) == 0 || len(excerpts) != len(job.pins) {
		return "", web.Fail(409, "事项或引用资料已变化，请重新核对")
	}
	instruction, err := s.Vault.Open(job.instruction, intakeScope(job.run)+"/instruction")
	if err != nil {
		return "", err
	}
	for i := range excerpts {
		excerpts[i] = web.TraceText(excerpts[i])
	}
	body, err := json.Marshal(map[string]any{"object": key, "type": kind, "intent": intent, "evidence": excerpts, "instruction": web.TraceText(instruction)})
	return string(body), err
}

func (s Server) saveCaseResult(ctx context.Context, tx pgx.Tx, job work, text string) error {
	if len(text) > 32<<10 || strings.ContainsRune(text, 0) {
		return web.Fail(422, "这一项的处理结果超出可保存范围")
	}
	cipher, err := s.Vault.Seal(web.TraceText(text), intakeScope(job.run)+"/result")
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "update intake_cases set revision=revision+1 where id=$1 and project_id=$2 and revision=$3",
		job.caseID, job.project, job.expected)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(409, "事项已补充新消息，请核对后再次处理")
	}
	_, err = tx.Exec(ctx, "update general_intake_work set result_cipher=$2 where run_id=$1", job.run, cipher)
	return err
}
