package identity

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func sealPrivateAssets(ctx context.Context, tx pgx.Tx, member string) (map[string]int64, error) {
	counts := make(map[string]int64)
	for _, step := range []struct{ name, query string }{
		{"sealedProjects", `update projects p set privacy_state='sealed_private',revision=p.revision+1,updated_at=clock_timestamp()
		 from spaces s where s.id=p.space_id and s.kind='personal' and s.owner_member_id=$1 and p.privacy_state='active'`},
		{"cancelledPrivateRuns", `update runs r set status='cancelled' from projects p,spaces s where r.project_id=p.id and p.space_id=s.id
		 and s.kind='personal' and s.owner_member_id=$1 and p.privacy_state='sealed_private' and r.status in ('queued','running','blocked','reconcile','paused')`},
		{"sealedCollections", `update kb_collections c set privacy_state='sealed_private',enabled=false,sync_status='paused',lease=null,scan_id=null,acl_revision=c.acl_revision+1
		 from spaces s where s.id=c.space_id and s.kind='personal' and s.owner_member_id=$1 and c.privacy_state='active'`},
		{"sealedDrafts", `update studio_drafts set privacy_state='sealed_private',revision=revision+1,updated_at=clock_timestamp()
		 where owner=$1 and privacy_state='active' and not studio_draft_shared(id)`},
		{"revokedOBOGrants", `delete from business_obo_grants where member_id=$1`},
		{"revokedSessions", `delete from sessions where member_id=$1`},
	} {
		tag, err := tx.Exec(ctx, step.query, member)
		if err != nil {
			return nil, err
		}
		counts[step.name] = tag.RowsAffected()
	}
	return counts, nil
}

func transferSharedAssets(ctx context.Context, tx pgx.Tx, departing, successor, team string) (map[string]int64, error) {
	counts := make(map[string]int64)
	const scope = " and s.kind='team' and ($3='' or s.team_id=nullif($3,'')::uuid)"
	for _, step := range []struct{ name, query string }{
		{"knowledgeCollections", `update kb_collections a set maintainer=$2 from spaces s where a.space_id=s.id and a.maintainer=$1` + scope},
		{"knowledgeDocuments", `update kb_documents a set maintainer=$2 from spaces s where a.space_id=s.id and a.maintainer=$1` + scope},
		{"businessConnectors", `update business_connectors a set maintainer=$2 from spaces s where a.space_id=s.id and a.maintainer=$1` + scope},
		{"businessBindings", `update business_connector_bindings a set owner_id=$2,revision=a.revision+1 from spaces s where a.space_id=s.id and a.owner_id=$1` + scope},
		{"writePolicies", `update business_mutation_policies a set maintainer=$2 from spaces s where a.space_id=s.id and a.maintainer=$1` + scope},
		{"eventTriggers", `with moved as (
		 update event_triggers t set maintainer=$2,revision=t.revision+1,notify_targets=coalesce((
		  select jsonb_agg(x order by x->>'channel',x->>'memberId') from (select distinct
		   case when j->>'memberId'=$1::uuid::text then jsonb_set(j,'{memberId}',to_jsonb($2::uuid::text)) else j end x
		   from jsonb_array_elements(t.notify_targets) j)a),'[]'::jsonb)
		  from spaces s where s.id=t.space_id and t.maintainer=$1` + scope + ` returning t.id,t.revision
		 ), queues as (
		  update event_trigger_batches b set trigger_revision=t.revision from moved t where b.trigger_id=t.id and b.status='queued'
		   and b.trigger_revision=t.revision-1
		 ) select count(*) from moved`},
	} {
		if step.name == "eventTriggers" {
			var count int64
			if err := tx.QueryRow(ctx, step.query, departing, successor, team).Scan(&count); err != nil {
				return nil, err
			}
			counts[step.name] = count
			continue
		}
		tag, err := tx.Exec(ctx, step.query, departing, successor, team)
		if err != nil {
			return nil, err
		}
		counts[step.name] = tag.RowsAffected()
	}
	tag, err := tx.Exec(ctx, `with moved as (
	 update pending_system_mutations a set approver_id=$2,status=case when r.actor=$1 then 'expired' else 'pending' end,
	  approved_by=null,decision_channel=null,decided_at=null,revision=a.revision+1
	  from runs r,projects p,spaces s where a.run_id=r.id and r.project_id=p.id and p.space_id=s.id and p.final_reviewer=$2
	   and a.approver_id=$1 and a.status in ('approved','pending')`+scope+` returning a.id
	 ) delete from business_mutation_notices where mutation_id in (select id from moved)`, departing, successor, team)
	if err != nil {
		return nil, err
	}
	counts["retiredReviewNotices"] = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `with selected as materialized (
	 select d.id,d.owner,d.revision,d.spec,coalesce(
	  (select r.spec from studio_releases r where r.draft_id=d.id and r.agent_scope='team' and r.state='published' order by r.revision desc limit 1),
	  (select p.spec from space_publication_drafts x join space_publications p on p.id=x.publication_id where x.draft_id=d.id limit 1)
	 ) as shared_spec from studio_drafts d where d.owner=$1 and d.privacy_state='active' and studio_draft_shared(d.id)
	  and ($3='' or d.team_id=nullif($3,'')::uuid)
	), sealed as (
	 insert into studio_sealed_versions(draft_id,revision,owner,spec)
	  select id,revision,owner,spec from selected where spec is distinct from shared_spec on conflict do nothing
	), pending as (
	 update studio_turns t set state='failed' from selected where t.draft_id=selected.id and t.state='working'
	) update studio_drafts d set owner=$2,spec=selected.shared_spec,revision=d.revision+1,shared_handoff_revision=d.revision+1,updated_at=clock_timestamp()
	 from selected where d.id=selected.id`, departing, successor, team)
	if err != nil {
		return nil, err
	}
	counts["drafts"] = tag.RowsAffected()
	return counts, nil
}
