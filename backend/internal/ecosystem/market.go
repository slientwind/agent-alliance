package ecosystem

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) marketCatalog(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var out json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_strip_nulls(jsonb_build_object(
	 'id',c.id,'name',coalesce(nullif(p.manifest->>'name',''),c.name),'kind',c.kind,'version',c.upstream_version,
	 'description',coalesce(p.manifest->>'description',c.description),'source',src.name,'homepage',c.homepage,
	 'reviewStatus',r.status,'license',r.report->>'license','activity',r.report->>'activity',
	 'package',p.id,'enabled',coalesce(b.enabled,false),'bindingRevision',coalesce(b.revision,0),
	 'retired',exists(select from enterprise_approved_packages old where old.catalog_id=c.id and old.revoked_at is not null),
	 'request',q.state,'reply',nullif(q.reason,''))
	 ) from ecosystem_catalog c join ecosystem_sources src on src.id=c.source_id
	 join ecosystem_candidate_reviews r on r.catalog_id=c.id
	 left join enterprise_approved_packages p on p.catalog_id=c.id and p.revoked_at is null and p.review_digest is not null
	 left join user_skill_bindings b on (b.member_id,b.approved_package_id)=($1,p.id)
	 left join ecosystem_admission_requests q on (q.requester,q.catalog_id)=($1,c.id)
	 where src.kind<>'private_git' or p.id is not null order by b.enabled desc nulls last,c.discovered_at desc,c.id limit 200))`,
		actor.ID).Scan(&out)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) requestAdmission(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return web.Fail(404, "找不到生态条目")
	}
	ctx := r.Context()
	var result string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var review, status string
		err := tx.QueryRow(ctx, `select ecosystem_review_digest(c.id),r.status
		 from ecosystem_catalog c join ecosystem_candidate_reviews r on r.catalog_id=c.id
		 join ecosystem_sources src on src.id=c.source_id and src.kind<>'private_git'
		 where c.id=$1 and not exists(select from enterprise_approved_packages p where p.catalog_id=c.id)
		 for update of c for share of r`, id).Scan(&review, &status)
		if err == pgx.ErrNoRows || err == nil && status == "blocked" {
			return web.Fail(409, "此条目当前无法申请准入")
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into ecosystem_admission_requests(catalog_id,requester,review_digest)
		 values($1,$2,$3) on conflict(catalog_id,requester) do nothing returning id`, id, actor.ID, review).Scan(&result)
		if err == pgx.ErrNoRows {
			return tx.QueryRow(ctx, "select id from ecosystem_admission_requests where (catalog_id,requester)=($1,$2)", id, actor.ID).Scan(&result)
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.package.request", id, map[string]string{"request": result})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"id": result})
}

func (s Server) returnRequests(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	var in approvalInput
	if err := web.ReadLimited(w, r, &in, 4096); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if _, err := uuid.Parse(id); err != nil || len(in.Expected) != 64 || in.Reason == "" ||
		utf8.RuneCountInString(in.Reason) > 800 || strings.ContainsRune(in.Reason, 0) {
		return web.Fail(422, "请核对检查报告并填写退回原因")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var matches bool
		if err := tx.QueryRow(ctx, "select ecosystem_review_digest(id)=$2 from ecosystem_catalog where id=$1 for update",
			id, in.Expected).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return web.Fail(409, "检查报告已更新，请重新核对")
		}
		tag, err := tx.Exec(ctx, `update ecosystem_admission_requests set state='returned',decider=$2,decided_at=clock_timestamp(),reason=$3
		 where catalog_id=$1 and state='pending'`, id, actor.ID, in.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "这些准入申请已处理")
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.request.return", id, map[string]int64{"count": tag.RowsAffected()})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s Server) setBinding(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	var in struct {
		Expected int
		Enabled  *bool
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if _, err := uuid.Parse(id); err != nil || in.Expected < 0 || in.Enabled == nil {
		return web.Fail(422, "请核对 Skill 版本和加载设置")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "select id from members where id=$1 and enabled and kind='person' for update", actor.ID).
			Scan(new(string)); err != nil {
			return err
		}
		var source, upstream, hash string
		var available bool
		err := tx.QueryRow(ctx, `select c.source_id,c.upstream_key,p.content_hash,p.revoked_at is null and p.review_digest is not null
		 from enterprise_approved_packages p join ecosystem_catalog c on c.id=p.catalog_id
		 where p.id=$1 and c.kind='skill' for share of p`, id).Scan(&source, &upstream, &hash, &available)
		if err == pgx.ErrNoRows {
			return web.Fail(404, "找不到已准入的 Skill")
		}
		if err != nil {
			return err
		}
		var previous bool
		err = tx.QueryRow(ctx, "select enabled,revision from user_skill_bindings where (member_id,approved_package_id)=($1,$2)", actor.ID, id).
			Scan(&previous, &revision)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if revision != in.Expected && !(revision == in.Expected+1 && previous == *in.Enabled) {
			return web.Fail(409, "Skill 加载设置已变化，请刷新")
		}
		if *in.Enabled && !available {
			return web.Fail(409, "此 Skill 已下架，无法加载")
		}
		if revision > 0 && previous == *in.Enabled || revision == 0 && !*in.Enabled {
			return nil
		}
		if *in.Enabled {
			if _, err := ReadFrozen(hash); err != nil {
				return web.Fail(409, "此 Skill 的已冻结文件无法通过校验")
			}
			if _, err := tx.Exec(ctx, `update user_skill_bindings b set enabled=false,revision=b.revision+1,updated_at=clock_timestamp()
			 from enterprise_approved_packages old join ecosystem_catalog c on c.id=old.catalog_id
			 where b.member_id=$1 and b.approved_package_id=old.id and old.id<>$2 and b.enabled
			  and (c.source_id,c.upstream_key)=($3,$4)`, actor.ID, id, source, upstream); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `select count(*) from user_skill_bindings b join enterprise_approved_packages p on p.id=b.approved_package_id
			 where b.member_id=$1 and b.enabled and p.revoked_at is null`, actor.ID).Scan(&count); err != nil {
				return err
			}
			if count >= 4 {
				return web.Fail(409, "最多同时加载 4 个 Skill，请先卸载一项")
			}
		}
		if err := tx.QueryRow(ctx, `insert into user_skill_bindings(member_id,approved_package_id,enabled) values($1,$2,$3)
		 on conflict(member_id,approved_package_id) do update set enabled=excluded.enabled,revision=user_skill_bindings.revision+1,
		   updated_at=clock_timestamp() returning revision`, actor.ID, id, in.Enabled).Scan(&revision); err != nil {
			return err
		}
		if *in.Enabled {
			if _, err := ContextForMember(ctx, tx, actor.ID); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.skill.binding", id, map[string]any{"revision": revision, "enabled": *in.Enabled})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"revision": revision, "enabled": *in.Enabled})
}
