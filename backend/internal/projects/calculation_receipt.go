package projects

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// Use character positions in extracted content, including Office location offsets.
func receiptExcerpt(text string, locations []byte, requested string, position *int) (int, int, error) {
	lines, err := knowledge.LocatedSentences(text, locations)
	if err != nil {
		return 0, 0, err
	}
	if position == nil {
		requested = strings.TrimSpace(requested)
	}
	if requested == "" && position == nil {
		if len(lines) != 1 {
			return 0, 0, web.Fail(422, "这份资料有多段内容，请选择一处要补充的原句")
		}
		requested = lines[0].Text
	}
	n := len([]rune(requested))
	if n == 0 || n > 320 || strings.ContainsAny(requested, "\r\n") {
		return 0, 0, web.Fail(422, "请选择一段不超过 320 字的连续原句")
	}
	if position != nil {
		for _, line := range lines {
			if *position < line.Start || *position > line.End || n > line.End-*position+1 {
				continue
			}
			offset := *position - line.Start
			if string([]rune(line.Text)[offset:offset+n]) == requested {
				return *position - 1, *position - 1 + n, nil
			}
		}
		return 0, 0, web.Fail(422, "原句与所选位置不一致，请重新核对固定版本")
	}
	start := -1
	for _, line := range lines {
		at := strings.Index(line.Text, requested)
		if at < 0 {
			continue
		}
		if start >= 0 || strings.LastIndex(line.Text, requested) != at {
			return 0, 0, web.Fail(422, "原句出现多次，请补充上下文以定位唯一出处")
		}
		start = line.Start - 1 + len([]rune(line.Text[:at]))
	}
	if start < 0 {
		return 0, 0, web.Fail(422, "所填原句无法在固定版本中定位")
	}
	return start, start + n, nil
}

type ReceiptSelection struct {
	Expected, Step, Version int
	Source, Excerpt         string
	ExcerptStart            *int `json:"excerptStart,omitempty"`
}

type ReceiptFile struct{ Name, Text string }

type ReceiptKind string

const (
	CalculationReceipt ReceiptKind = "calculation"
	LookupReceipt      ReceiptKind = "lookup"
)

func (s Server) calculationReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.submitSourceReceipt(w, r, u, CalculationReceipt)
}

func (s Server) lookupReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.submitSourceReceipt(w, r, u, LookupReceipt)
}

func (s Server) submitSourceReceipt(w http.ResponseWriter, r *http.Request, u identity.Member, kind ReceiptKind) error {
	var in ReceiptSelection
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 || in.Step < 1 || in.Version < 1 || len(in.Excerpt) > 1600 {
		return web.Fail(422, "需要指定补件步骤、来源版本与提交标识")
	}
	ctx := r.Context()
	run := r.PathValue("id")
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		revision, err = ApplySourceReceipt(ctx, tx, run, u.ID, key, kind, in, nil)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"controlRevision": revision})
}

// ApplySourceReceipt shares one transaction for source registration, version
// binding, the control receipt and resumption of the originally authorized step.
func ApplySourceReceipt(ctx context.Context, tx pgx.Tx, run, actor, key string, kind ReceiptKind, in ReceiptSelection, file *ReceiptFile) (int, error) {
	var operation, action, note string
	switch kind {
	case CalculationReceipt:
		operation, action, note = "receipt", "calculation.receipt", "按所选原句继续核算"
	case LookupReceipt:
		operation, action, note = "lookup-receipt", "lookup.receipt", "按所选原句继续回答"
	default:
		return 0, web.Fail(422, "未知补件类型")
	}
	sign := web.Hash(fmt.Sprintf("%s/%s/%d/%d/%s/%d/%s", run, operation, in.Expected, in.Step, in.Source, in.Version, in.Excerpt))
	if in.ExcerptStart != nil {
		sign = web.Hash(fmt.Sprintf("%x/position/%d", sign, *in.ExcerptStart))
	}
	if file != nil {
		sign = web.Hash(fmt.Sprintf("%x/file/%x/%x", sign, web.Hash(file.Name), web.Hash(file.Text)))
	}
	var revision int
	var project, state string
	var version int
	if err := tx.QueryRow(ctx, `select r.project_id,r.status,r.plan_version,r.control_revision from runs r join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version) where r.id=$1 and r.actor=$2 and p.owner=$2 and case when $3='lookup' then r.await_lookup_receipt else r.await_calculation_receipt end and exists(select from project_members pm where pm.project_id=p.id and pm.member_id=$2) for update of r,p`, run, actor, kind).Scan(&project, &state, &version, &revision); err != nil {
		return 0, err
	}
	var permitted bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&permitted); err != nil {
		return 0, err
	}
	if !permitted {
		return 0, web.Fail(403, "此任务的服务授权已失效")
	}
	var same bool
	err := tx.QueryRow(ctx, "select signature=$1,revision from controls where actor=$2 and request_key=$3", sign, actor, key).Scan(&same, &revision)
	if err == nil {
		if !same {
			return 0, web.Fail(409, "此提交标识已用于不同补件")
		}
		return revision, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}
	var waiting bool
	if err := tx.QueryRow(ctx, "select exists(select from calculation_requests where run_id=$1 and ordinal=$2 and kind=$3)", run, in.Step, kind).Scan(&waiting); err != nil {
		return 0, err
	}
	if !waiting || in.Expected != revision || state != "blocked" {
		return 0, web.Fail(409, "待补件步骤或运行状态已变化，请重新核对")
	}
	var body, digest string
	var locations []byte
	if file == nil {
		err = tx.QueryRow(ctx, `select v.extracted,v.extraction_locations,v.object_hash from sources s join source_versions v on v.source_id=s.id and v.version=$3 where s.project_id=$1 and s.id::text=$2 and s.revoked_at is null and v.extracted is not null and s.current_version=v.version for share of s`, project, in.Source, in.Version).Scan(&body, &locations, &digest)
		if err == pgx.ErrNoRows {
			return 0, web.Fail(409, "补件已换版、不可读或不属于此项目")
		}
		if err != nil {
			return 0, err
		}
	} else {
		body = file.Text
		digest = fmt.Sprintf("%x", web.Hash(body))
	}
	start, end, err := receiptExcerpt(body, locations, in.Excerpt, in.ExcerptStart)
	if err != nil {
		return 0, err
	}
	if kind == CalculationReceipt {
		amounts := knowledge.MoneyInExcerpt(body, start, end)
		if len(amounts) == 0 {
			return 0, web.Fail(422, "原句没有可核算的带币种金额，请核对后重新选择")
		}
		visible := knowledge.MoneyInExcerpt(string([]rune(body)[start:end]), 0, end-start)
		if len(amounts) != len(visible) {
			return 0, web.Fail(422, "请选全金额、正负号和币种所在的原句")
		}
		for i, m := range amounts {
			if m.Currency != visible[i].Currency || m.Cents.Cmp(visible[i].Cents) != 0 {
				return 0, web.Fail(422, "请选全金额、正负号和币种所在的原句")
			}
		}
	}
	var bound bool
	if err = tx.QueryRow(ctx, `select exists(select from plan_sources pin join source_versions v on (v.source_id,v.version)=(pin.source_id,pin.source_version) where pin.project_id=$1 and pin.plan_version=$2 and (pin.source_id::text=$3 or v.object_hash=$4))`, project, version, in.Source, digest).Scan(&bound); err != nil {
		return 0, err
	}
	if bound {
		return 0, web.Fail(409, "此原件已在本方案中，请核对并补充另一份原件")
	}
	if file != nil {
		in.Source, err = knowledge.ConfiguredObjects().NewTextSource(ctx, tx, project, actor, file.Name, file.Text)
		if err != nil {
			return 0, err
		}
		in.Version = 1
	}
	next := version + 1
	for _, q := range []string{
		"insert into plans(project_id,version,brief,confirmed_by,suggestion_id) select project_id,$3,brief,confirmed_by,suggestion_id from plans where project_id=$1 and version=$2",
		"insert into nodes select project_id,$3,ordinal,agent_id,agent_revision,lookup_phase from nodes where project_id=$1 and plan_version=$2",
		"insert into plan_delivery_contracts select project_id,$3,allow_partial from plan_delivery_contracts where project_id=$1 and plan_version=$2",
		"insert into plan_delivery_requirements select project_id,$3,item,ordinal,label from plan_delivery_requirements where project_id=$1 and plan_version=$2",
		"insert into plan_tool_choices select project_id,$3,ordinal,selector,alternative,alternative_revision from plan_tool_choices where project_id=$1 and plan_version=$2",
		"insert into node_edges select project_id,$3,ordinal,depends_on from node_edges where project_id=$1 and plan_version=$2",
		"insert into plan_rules select project_id,$3,rule_id,rule_revision from plan_rules where project_id=$1 and plan_version=$2",
		"insert into plan_preferences select project_id,$3,preference_id,revision from plan_preferences where project_id=$1 and plan_version=$2",
		"insert into plan_sources select project_id,$3,source_id,source_version,receipt_step,receipt_order,excerpt_start,excerpt_end,receipt_kind,picture_order from plan_sources where project_id=$1 and plan_version=$2",
	} {
		if _, err = tx.Exec(ctx, q, project, version, next); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, "insert into plan_sources(project_id,plan_version,source_id,source_version,receipt_step,receipt_order,excerpt_start,excerpt_end,receipt_kind) values($1,$2,$3,$4,$5,$2,$6,$7,$8)", project, next, in.Source, in.Version, in.Step, start, end, kind); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "update projects set plan_version=$2 where id=$1", project, next); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "update runs set plan_version=$2 where id=$1", run, next); err != nil {
		return 0, err
	}
	if _, _, err = knowledge.Inputs(ctx, tx, run); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "delete from calculation_requests where run_id=$1 and ordinal=$2", run, in.Step); err != nil {
		return 0, err
	}
	if err = ClearLookupIndexGate(ctx, tx, run); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "delete from lookup_index_waits where run_id=$1 and ordinal=$2", run, in.Step); err != nil {
		return 0, err
	}
	revision++
	if _, err = tx.Exec(ctx, "update runs set status='running',control_revision=$2 where id=$1", run, revision); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "insert into controls(run_id,actor,request_key,signature,action,reason,revision) values($1,$2,$3,$4,$5,$6,$7)", run, actor, key, sign, action, note, revision); err != nil {
		return 0, err
	}
	if err = TouchRun(ctx, tx, run, project, "running"); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "select record_service_event($1,'step','receipt_submitted',$2)", run, in.Step); err != nil {
		return 0, err
	}
	return revision, database.Audit(ctx, tx, actor, action, run, map[string]any{"plan": next, "previous": version, "step": in.Step, "source": in.Source, "version": in.Version, "from": start + 1, "to": end})
}
