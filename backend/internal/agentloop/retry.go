package agentloop

import (
	"net/http"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/web"
	"alliance/internal/websearch"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) retryRun(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var body struct{ Expected int }
	if err := web.ReadLimited(w, r, &body, 1024); err != nil {
		return err
	}
	project, oldRun := r.PathValue("id"), r.PathValue("run")
	for _, id := range []string{project, oldRun} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return web.Fail(404, "找不到这条答复")
		}
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 1 || len(key) > 128 || body.Expected < 1 {
		return web.Fail(422, "请核对当前对话后重试")
	}
	ctx := r.Context()
	var run string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		in := StartInput{Expected: body.Expected}
		var sourcePlan int
		var capabilities []string
		err := tx.QueryRow(ctx, `select plan.brief,g.tier,g.direct_external,g.enabled_capabilities,coalesce(g.studio_release::text,''),
		 r.plan_version,to_jsonb(array(select jsonb_build_object('id',a.agent_id,'revision',a.revision)
		  from general_external_agents a where a.run_id=r.id order by a.agent_id))
		 from general_agent_runs g join runs r on r.id=g.run_id join projects p on p.id=r.project_id and p.owner=$3
		 join project_members pm on (pm.project_id,pm.member_id)=(p.id,$3)
		 join plans plan on (plan.project_id,plan.version)=(r.project_id,r.plan_version)
		 where (r.id,r.project_id,r.actor)=($1,$2,$3) and r.status in ('completed','failed','cancelled')`,
			oldRun, project, actor.ID).Scan(&in.Goal, &in.Tier, &in.Direct, &capabilities, &in.StudioRelease, &sourcePlan, &in.External)
		if err != nil {
			return err
		}
		if in.StudioRelease == "" {
			if capabilities != nil {
				in.Capabilities = &capabilities
			}
		} else {
			// StartTx reloads and validates the exact publication.
			in.Tier = ""
		}
		rows, err := tx.Query(ctx, `select source_id,source_version from plan_sources where (project_id,plan_version)=($1,$2)
		 order by source_id`, project, sourcePlan)
		if err != nil {
			return err
		}
		in.Sources, err = pgx.CollectRows(rows, pgx.RowToStructByPos[harness.SourceRevision])
		if err != nil {
			return err
		}
		in.SearchQuery, err = websearch.ReplayQuery(ctx, tx, s.Vault, oldRun)
		if err != nil {
			return err
		}
		run, err = s.StartTx(ctx, tx, project, actor.ID, "retry/"+oldRun+"/"+key, in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into run_quality_signals(run_id,actor,kind)
		 values($1,$2,'reply_regenerated') on conflict do nothing`, oldRun, actor.ID)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"id": run})
}
