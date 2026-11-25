package resources

import (
	"context"
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (c Catalog) externalAdmissions(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var data json.RawMessage
	err := c.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_strip_nulls(jsonb_build_object(
	 'id',a.id,'revision',r.revision,'name',r.name,'description',r.instructions,'team',a.team_id,'phase',d.phase,
	 'published',a.published_revision=r.revision,'owner',a.owner=$1,'testersRevision',x.testers_revision,
	 'testers',case when a.owner=$1 then array(select member_id from external_agent_testers t
	  where (t.agent_id,t.revision)=(a.id,r.revision) order by member_id) end,
	 'template',coalesce(r.card#>>'{allianceAdapter,template}','a2a')))
	 from external_agent_admissions x
	 join agent_revisions r using(agent_id,revision) join agents a on a.id=r.agent_id
	 cross join lateral (select external_admission_phase(a.id,r.revision) phase) d
	 where not a.removed and (r.revision=a.published_revision or r.revision=(select max(other.revision) from agent_revisions other where other.agent_id=a.id))
	 and ((a.owner=$1 and exists(select from team_members tm where (tm.team_id,tm.member_id)=(a.team_id,$1)))
	  or (d.phase in ('beta','pending_verify') and external_admission_can_use(a.id,r.revision,$1))
	  or (d.phase='verified' and a.published_revision=r.revision and external_admission_can_use(a.id,r.revision,$1)))
	 order by r.name,r.revision desc,a.id limit 250))`, actor.ID).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

func (c Catalog) externalVerificationQueue(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var data json.RawMessage
	err := c.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_build_object('id',q.id,'agent',a.id,
	 'revision',q.revision,'name',r.name,'description',r.instructions,'team',a.team_id,'submittedAt',q.requested_at,
	 'buildDigest',q.build_digest,'evidenceDigest',q.evidence_digest,'state',external_admission_phase(a.id,q.revision))
	 from agent_release_requests q join external_agent_admissions x using(agent_id,revision)
	 join agent_revisions r using(agent_id,revision) join agents a on a.id=q.agent_id
	 where q.state='pending' and q.revision=(select max(other.revision) from agent_revisions other where other.agent_id=a.id)
	   and not a.removed order by q.requested_at,q.id limit 200))`).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

func (c Catalog) externalTesters(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Revision, Expected int
		Members            []string
	}
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil || in.Revision < 1 || in.Expected < 1 || len(in.Members) > 50 {
		return web.Fail(422, "请核对测试组和 Agent 版本")
	}
	members := make([]string, 0, len(in.Members))
	seen := map[uuid.UUID]bool{}
	for _, raw := range in.Members {
		member, err := uuid.Parse(raw)
		if err != nil || member == uuid.Nil || seen[member] || member.String() == actor.ID {
			return web.Fail(422, "请选择不同的团队成员加入测试组")
		}
		seen[member] = true
		members = append(members, member.String())
	}
	ctx := r.Context()
	var rev int
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var team, phase string
		err := tx.QueryRow(ctx, `select a.team_id,x.testers_revision,external_admission_phase(a.id,x.revision)
			from external_agent_admissions x join agents a on a.id=x.agent_id
			join team_members tm on (tm.team_id,tm.member_id)=(a.team_id,$3)
			where (x.agent_id,x.revision,a.owner)=($1,$2,$3) and not a.disabled and not a.removed
			 and x.revision=(select max(revision) from agent_revisions where agent_id=a.id) for update of a,x`,
			id, in.Revision, actor.ID).Scan(&team, &rev, &phase)
		if err == pgx.ErrNoRows {
			return web.Fail(404, "找不到可编辑的测试组")
		}
		if err != nil {
			return err
		}
		if rev != in.Expected || phase == "verified" {
			return web.Fail(409, "测试组或上架状态已变化，请刷新")
		}
		var active int
		if err := tx.QueryRow(ctx, `select count(*) from team_members tm join members u on u.id=tm.member_id
			where tm.team_id=$1 and u.enabled and u.kind='person' and u.id=any($2::uuid[])`, team, members).Scan(&active); err != nil {
			return err
		}
		if active != len(members) {
			return web.Fail(409, "测试组中有成员不在当前团队")
		}
		if _, err := tx.Exec(ctx, "delete from external_agent_testers where (agent_id,revision)=($1,$2)", id, in.Revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into external_agent_testers(agent_id,revision,member_id)
			select $1,$2,unnest($3::uuid[])`, id, in.Revision, members); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `update external_agent_admissions set testers_revision=testers_revision+1
			where (agent_id,revision)=($1,$2) returning testers_revision`, id, in.Revision).Scan(&rev); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "external_agent.testers", id, map[string]any{"revision": in.Revision, "members": members})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": rev})
}

func lockVerificationAgent(ctx context.Context, tx pgx.Tx, actor, id string, revision int) error {
	var enrolled bool
	if err := tx.QueryRow(ctx, "select exists(select from external_agent_admissions where (agent_id,revision)=($1,$2))", id, revision).
		Scan(&enrolled); err != nil {
		return err
	}
	if !enrolled {
		return lockTeamAgent(ctx, tx, actor, id, "")
	}
	return tx.QueryRow(ctx, "select 1 from agents where id=$1 and not disabled and not removed and not emergency for update", id).Scan(new(int))
}
