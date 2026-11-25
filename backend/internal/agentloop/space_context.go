package agentloop

import (
	"context"
	"encoding/json"
	"slices"

	"alliance/internal/capability"
	"alliance/internal/glossary"
	"github.com/jackc/pgx/v5"
)

type spaceContextLayer struct {
	ID   string
	Kind string
	Body []byte
}

func readSpaceContext(ctx context.Context, db glossary.Querier, space string, run runData) ([]spaceContextLayer, error) {
	rows, err := db.Query(ctx, `select s.id,s.kind,jsonb_strip_nulls(jsonb_build_object(
	 'space',s.name,'revision',s.revision,'instructions',nullif(left(s.custom_instructions,1500),''),
	 'businessViews',case when $3 then (select jsonb_agg(to_jsonb(v)) from (
	   select v.id,v.view_name name,left(v.business_desc,160) description,v.revision from semantic_data_views v
	   join business_connectors c on c.id=v.connector_id where v.space_id=s.id and v.enabled and c.enabled
	    and v.sensitivity in ('public','internal') order by v.view_name,v.id limit 8)v) end,
	 'glossary',case when $3 then (select jsonb_agg(to_jsonb(g)) from (
	   select g.term,left(g.definition,200) definition,g.revision from enterprise_glossary g where g.space_id=s.id and g.enabled
	   and (strpos(lower($5),lower(g.term))>0 or exists(select from unnest(g.aliases) alias where strpos(lower($5),lower(alias))>0))
	   order by g.term,g.id limit 8)g) end,
	 'defaultTemplates',case when $4 then (select jsonb_agg(to_jsonb(t)) from (
	   select t.id,t.doc_type format,t.name,t.revision from corporate_templates t where t.space_id=s.id and t.archived_at is null
	    and (t.is_default or t.id=s.default_template_id) order by (t.id=s.default_template_id) desc,t.doc_type,t.id limit 4)t) end
	 )) from spaces s where (s.id=$1 or s.kind='commons') and space_role($1,$2) in ('member','space_admin')
	  and space_role(s.id,$2) in ('member','space_admin') order by (s.id=$1) desc,s.id`,
		space, run.actor, capability.Allows(run.capabilities, capability.Knowledge), capability.Allows(run.capabilities, capability.Office), boundedText(run.goal, 8192))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[spaceContextLayer])
}

func layerHasContent(body []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	return len(fields) > 2
}

func sameSpaceContext(a, b []spaceContextLayer) bool {
	return slices.EqualFunc(a, b, func(x, y spaceContextLayer) bool {
		return x.ID == y.ID && x.Kind == y.Kind && slices.Equal(x.Body, y.Body)
	})
}
