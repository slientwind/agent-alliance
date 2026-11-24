package runtime

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"path/filepath"
	"strings"

	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"github.com/jackc/pgx/v5"
)

type toolChoice struct {
	target  int
	profile string
	options harness.ReportOptions
	office  harness.OfficeOptions
}

func (c *toolChoice) load(ctx context.Context, tx pgx.Tx, run string, selector int) error {
	var raw []byte
	err := tx.QueryRow(ctx, `select route.ordinal,coalesce(max(tool_choice_profile(ar.tool)) filter(where picked.position=1),'aggregate'),jsonb_agg(jsonb_build_object(
   'name',ar.name,'revision',ar.revision,'group',ar.tool->'config'->>'group','amount',ar.tool->'config'->>'amount',
   'filterColumn',ar.tool->'config'->>'filterColumn','filterValue',ar.tool->'config'->>'filterValue','unitColumn',ar.tool->'config'->>'unitColumn',
   'title',case when tool_choice_profile(ar.tool) in ('docx','xlsx','pptx') then coalesce(nullif(ar.tool->'config'->>'title',''),'工作成果') end) order by picked.position)
 from runs r join plan_tool_choices route on (route.project_id,route.plan_version)=(r.project_id,r.plan_version)
 join nodes n on (n.project_id,n.plan_version,n.ordinal)=(route.project_id,route.plan_version,route.ordinal)
 cross join lateral (values (1,n.agent_id,n.agent_revision),(2,route.alternative,route.alternative_revision)) picked(position,agent,rev)
 join agent_revisions ar on (ar.agent_id,ar.revision)=(picked.agent,picked.rev)
 where r.id=$1 and route.selector=$2 group by route.ordinal`, run, selector).Scan(&c.target, &c.profile, &raw)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if c.profile == "aggregate" {
		return json.Unmarshal(raw, &c.options)
	}
	return json.Unmarshal(raw, &c.office)
}

func (c toolChoice) input(goal string, files []knowledge.InputSource) string {
	if c.office != nil {
		sources := make([]harness.OfficeSource, 0, len(files))
		for _, file := range files {
			sources = append(sources, harness.OfficeSource{Name: file.Name, Version: file.Version})
		}
		return c.office.Input(goal, c.profile, sources)
	}
	headers := make([]harness.ReportHeader, 0, len(files))
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f.Name), ".csv") {
			if cols, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(f.Text, "\ufeff"))).Read(); err == nil {
				headers = append(headers, harness.ReportHeader{Name: f.Name, Columns: cols})
			}
		}
	}
	return c.options.Input(goal, headers)
}

func (c toolChoice) instructions() string {
	if c.office != nil {
		return harness.OfficeChoiceInstructions
	}
	return harness.ReportChoiceInstructions
}

func (c toolChoice) accept(reply, finishReason string) (int, string) {
	if c.office != nil {
		return c.office.Accept(reply, finishReason == "" || finishReason == "stop")
	}
	return c.options.Accept(reply)
}

func (c toolChoice) record(ctx context.Context, tx pgx.Tx, step Step, choice int) error {
	if _, err := tx.Exec(ctx, `insert into run_tool_choices (run_id,ordinal,choice) values ($1,$2,$3)`, step.Run, c.target, choice); err != nil {
		return err
	}
	code := "tool_choice_unavailable"
	if choice > 0 {
		code = "tool_choice_recorded"
	}
	_, err := tx.Exec(ctx, `insert into trace_events(run_id,ordinal,kind,code) values ($1,$2,'checkpoint',$3)`, step.Run, step.Ordinal, code)
	return err
}
