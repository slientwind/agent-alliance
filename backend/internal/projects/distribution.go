package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (s Server) reviewDistribution(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Source, Artifact, Decision, Reason string
		Version, Expected                  int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > 2000 || strings.ContainsRune(in.Reason, 0) || (in.Decision != "reference" && in.Decision != "invalid" && in.Decision != "restricted") {
		return web.Fail(422, "请选择分发处理方式并说明依据（最多 2000 字）")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		var revision, step int
		var author, run string
		err = tx.QueryRow(ctx, `select c.revision,c.created_by,a.ordinal,a.run_id from distribution_cases c join artifacts a on a.id=c.artifact_id where c.project_id=$1 and c.source_id=$2 and c.artifact_id=$3 and c.version=$4`, p.ID, in.Source, in.Artifact, in.Version).Scan(&revision, &author, &step, &run)
		if err != nil {
			return err
		}
		if u.ID != author && u.ID != p.Owner {
			return web.Fail(403, "需要资料负责人或项目发起人核对分发范围")
		}
		if revision != in.Expected {
			return web.Fail(409, "该版本的分发决定已更新，请重新核对")
		}
		_, err = tx.Exec(ctx, `insert into distribution_reviews(source_id,artifact_id,version,revision,actor,decision,reason) values($1,$2,$3,$4,$5,$6,$7)`, in.Source, in.Artifact, in.Version, revision+1, u.ID, in.Decision, in.Reason)
		if err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "select record_service_event($1,'distribution',$2,$3,$4,$5)", run, in.Decision, step, in.Artifact, in.Version); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "delivery.distribution", in.Artifact, map[string]any{"source": in.Source, "version": in.Version, "decision": in.Decision, "revision": revision + 1})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

const distributionCasesSQL = `array(select jsonb_build_object('source',c.source_id,'sourceName',c.source_name,'artifact',c.artifact_id,'name',a.name,
 'version',c.version,'step',a.ordinal,'state',c.state,'revision',c.revision,'canManage',($1 in(c.created_by,p.owner)),
 'maintainer',(select display_name from members where id=c.created_by),'owner',(select display_name from members where id=p.owner),
 'history',case when $1 in(c.created_by,p.owner) then array(select jsonb_build_object('revision',v.revision,'actor',u.display_name,'time',v.created_at,'state',v.decision,'reason',v.reason) from distribution_reviews v join members u on u.id=v.actor where(v.source_id,v.artifact_id,v.version)=(c.source_id,c.artifact_id,c.version) order by v.revision desc) end,
 'current',p.stage='review' and a.current_version=c.version,
 'editions',array(select d.edition from deliveries d join delivery_items di on di.delivery_id=d.id join delivery_distribution_inputs e on e.delivery_id=d.id
 where e.source_id=c.source_id and (di.artifact_id,di.version)=(c.artifact_id,c.version) order by d.edition))
 from distribution_cases c join artifacts a on a.id=c.artifact_id where c.project_id=p.id order by a.ordinal,c.version,c.source_id)`
