package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (s Server) review(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.artifactAction(w, r, u, false)
}
func (s Server) revise(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.artifactAction(w, r, u, true)
}
func (s Server) artifactAction(w http.ResponseWriter, r *http.Request, u identity.Member, edit bool) error {
	var in struct {
		Version                  int
		Body, Reason, Assignment string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if edit && (web.Blank(in.Body) || web.Blank(in.Reason)) {
		return web.Fail(422, "请说明修改原因并提供修订正文")
	}
	ctx := r.Context()
	id := r.PathValue("artifact")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Stage != "review" {
			return web.Fail(409, "当前项目未处于审核阶段")
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "select can_review from project_members where project_id=$1 and member_id=$2", p.ID, u.ID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return web.Fail(403, "需要项目审核权限")
		}
		var ver, ord int
		var run string
		if err := tx.QueryRow(ctx, "select current_version,ordinal,run_id from artifacts where id=$1 and project_id=$2 for update", id, p.ID).Scan(&ver, &ord, &run); err != nil {
			return err
		}
		if in.Version != ver {
			return web.Fail(409, "成果已更新，请查看当前版本")
		}
		if edit {
			var binary bool
			if err := tx.QueryRow(ctx, "select exists(select from artifact_files where artifact_id=$1 and version=$2)", id, ver).Scan(&binary); err != nil {
				return err
			}
			if binary {
				return web.Fail(409, "文件型成果请上传外部修订文件")
			}
			if _, err = tx.Exec(ctx, "insert into artifact_versions(artifact_id,version,body,reason) values($1,$2,$3,$4)", id, ver+1, in.Body, in.Reason); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "update artifacts set current_version=current_version+1 where id=$1", id); err != nil {
				return err
			}
			if err := advanceDescendants(ctx, tx, run, ord); err != nil {
				return err
			}
		} else {
			var pending bool
			if err = tx.QueryRow(ctx, "select exists(select from feedback where artifact_id=$1 and "+activeFeedback+")", id).Scan(&pending); err != nil {
				return err
			}
			if pending {
				return web.Fail(409, "此版本仍有修改或验收问题待复核")
			}
			if err = tx.QueryRow(ctx, "select source_check_needed($1)", id).Scan(&pending); err != nil {
				return err
			}
			if pending {
				return web.Fail(409, "请先核对资料变更对当前成果的影响")
			}
			if _, err = tx.Exec(ctx, "insert into reviews(artifact_id,version,reviewer) values($1,$2,$3) on conflict do nothing", id, ver, u.ID); err != nil {
				return err
			}
			if in.Assignment != "" {
				tag, err := tx.Exec(ctx, `insert into takeover_reviews(assignment,artifact_id,version,reviewer)
 select h.id,$2,$3,$4 from projects p join reviewer_handoffs h on h.id=p.reviewer_assignment
 where p.id=$1 and h.id=$5 and h.emergency and h.state='accepted' and h.successor=$4 and p.final_reviewer=$4 on conflict do nothing`, p.ID, id, ver, u.ID, in.Assignment)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return web.Fail(409, "接管职责或本版本核对状态已变化，请刷新")
				}
			}
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		action := "artifact.review"
		if edit {
			action = "artifact.revise"
		}
		return database.Audit(ctx, tx, u.ID, action, id, map[string]any{"base": ver, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) deliver(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected int
		Handoff  *struct {
			Purpose      string `json:"purpose,omitempty"`
			Instructions string `json:"instructions,omitempty"`
			Limitations  string `json:"limitations,omitempty"`
		}
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Handoff != nil {
		for _, field := range []*string{&in.Handoff.Purpose, &in.Handoff.Instructions, &in.Handoff.Limitations} {
			*field = strings.TrimSpace(*field)
			if utf8.RuneCountInString(*field) > 2000 || strings.ContainsRune(*field, 0) {
				return web.Fail(422, "交付说明每项最多 2000 字且不能包含空字符")
			}
		}
	}
	ctx := r.Context()
	var delivery string
	var pack *knowledge.DeliveryPackage
	defer func() {
		if pack != nil {
			pack.Close()
		}
	}()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Reviewer != u.ID {
			return web.Fail(403, "需要最终确认人")
		}
		if p.Stage != "review" || p.Revision != in.Expected {
			return web.Fail(409, "项目状态或版本已更新")
		}
		var pending bool
		if err = tx.QueryRow(ctx, "select exists(select from feedback f join artifacts a on a.id=f.artifact_id where a.project_id=$1 and "+activeFeedback+")", p.ID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return web.Fail(409, "请先由负责审核人处理未关闭的修改与验收问题")
		}
		var sourcePending bool
		if err := tx.QueryRow(ctx, "select exists(select from changed_artifact_sources where project_id=$1 and decision is distinct from 'retain')", p.ID).Scan(&sourcePending); err != nil {
			return err
		}
		if sourcePending {
			return web.Fail(409, "请先核对资料变更对当前成果的影响")
		}
		var held bool
		if err := tx.QueryRow(ctx, "select candidate_distribution_held($1)", p.ID).Scan(&held); err != nil {
			return err
		}
		if held {
			return web.Fail(409, "当前成果版本仍有撤权后的分发事项待处理")
		}
		var total, approved int
		if err := tx.QueryRow(ctx, `select count(*),count(r.reviewer) filter(where not takeover_review_needed(a.project_id,a.id,a.current_version)) from artifacts a left join reviews r on(r.artifact_id,r.version)=(a.id,a.current_version) where a.project_id=$1`, p.ID).Scan(&total, &approved); err != nil {
			return err
		}
		if total == 0 || approved != total {
			return web.Fail(409, "请先审核所有当前成果版本")
		}
		if err := tx.QueryRow(ctx, `insert into deliveries(project_id,confirmed_by,reviewer_assignment,handoff,edition)
 select p.id,$2,p.reviewer_assignment,coalesce(nullif($3::jsonb,'null'::jsonb),case when distribution_state(prev.id) in('clear','reference') then prev.handoff end,'{}'::jsonb),coalesce(prev.edition,0)+1
 from projects p left join lateral (select id,edition,handoff from deliveries where project_id=p.id order by edition desc limit 1) prev on true
 where p.id=$1 and (prev.id is null or exists(select from delivery_revision_requests q join feedback f on f.id=q.feedback_id
 where q.base_delivery=prev.id and f.state='closed' and f.candidate_version>f.base_version)) returning id`, p.ID, u.ID, in.Handoff).Scan(&delivery); err != nil {
			if err == pgx.ErrNoRows {
				return web.Fail(409, "关联修订尚待负责审核人复核")
			}
			return err
		}
		if _, err := tx.Exec(ctx, "insert into delivery_items select $1,id,current_version from artifacts where project_id=$2", delivery, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into delivery_source_evidence(delivery_id,source_id,pinned,observed,revoked,assessments,revision_inputs,calculation_receipt,lookup_receipt)
 select $1,s.id,ps.source_version,s.current_version,s.revoked_at is not null,
 coalesce((select jsonb_agg(jsonb_build_object('artifact',c.artifact_id,'version',c.artifact_version,'id',a.id,'reviewer',a.actor,'decision',a.decision,'time',a.created_at) order by c.artifact_id) from changed_artifact_sources c join source_assessments a on a.id=c.assessment where c.project_id=p.id and c.source_id=s.id and c.decision='retain'),'[]'),
 coalesce((select jsonb_agg(cr.receipt order by cr.artifact_id,cr.applied_version) from change_source_receipts cr join delivery_items di on di.artifact_id=cr.artifact_id and di.delivery_id=$1 where cr.source_id=s.id and cr.applied_version<=di.version),'[]')
  ,case when ps.receipt_kind='calculation' then receipt.pin end
  ,case when ps.receipt_kind='lookup' then receipt.pin end
 from projects p join plan_sources ps on(ps.project_id,ps.plan_version)=(p.id,p.plan_version) join sources s on s.id=ps.source_id
 cross join lateral(select case when ps.receipt_step is not null then
   jsonb_build_object('step',ps.receipt_step,'plan',ps.receipt_order,'from',ps.excerpt_start+1,'to',ps.excerpt_end) end as pin) receipt
 where p.id=$2`, delivery, p.ID); err != nil {
			return err
		}
		pack, err = knowledge.PrepareDeliveryPackage(ctx, tx, knowledge.ConfiguredObjects(), delivery)
		if err != nil {
			return err
		}
		var authorized bool
		if err = tx.QueryRow(ctx, "select exists(select from project_members where project_id=$1 and member_id=$2 and can_review)", p.ID, u.ID).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return web.Fail(403, "当前项目审核授权已结束")
		}
		if err := Touch(ctx, tx, p.ID, "delivered"); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "delivery.confirm", delivery, map[string]int{"items": total})
	})
	if err != nil {
		return err
	}
	if r.URL.Query().Get("download") == "1" {
		pack.Serve(w, r)
		return nil
	}
	return web.JSON(w, 201, map[string]string{"id": delivery})
}
func (s Server) deliveryReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	edition, err := knowledge.DeliveryEdition(r)
	if err != nil {
		return err
	}
	var data json.RawMessage
	err = s.DB.QueryRow(r.Context(), `select jsonb_build_object('edition',d.edition,'distribution',distribution_state(d.id),'previous',c.previous,'changes',case when distribution_state(d.id) in('clear','reference') and (c.previous is null or distribution_state(c.previous) in('clear','reference')) then to_jsonb(c.changes) else '[]'::jsonb end,'comparisonHeld',c.previous is not null and distribution_state(c.previous) not in('clear','reference'),'changedNotes',to_jsonb(c.changed_notes),'handoff',case when distribution_state(d.id) in('clear','reference') then d.handoff else '{}'::jsonb end,'items',jsonb_agg(
     jsonb_build_object('step',i.ordinal,'name',i.name,'version',i.version,'dependencies',i.dependencies) order by i.ordinal))
     from deliveries d join delivery_comparisons c on c.id=d.id join project_members m on m.project_id=d.project_id join delivery_recipient_items i on i.delivery_id=d.id
     where m.member_id=$1 and d.project_id=$2 and d.edition=$3 group by d.id,c.previous,c.changes,c.changed_notes`, u.ID, r.PathValue("id"), edition).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}
func (s Server) download(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	edition, err := knowledge.DeliveryEdition(r)
	if err != nil {
		return err
	}
	var data []byte
	var status string
	err = s.DB.QueryRow(r.Context(), `select distribution_state(d.id),case when distribution_state(d.id) in('clear','reference') then jsonb_build_object('id',d.id,'confirmedBy',d.confirmed_by,'confirmedAt',d.created_at,'edition',c.edition,'previous',c.previous,'changes',to_jsonb(c.changes),'changedNotes',to_jsonb(c.changed_notes),'handoff',d.handoff,'items',d.items) end
 from delivery_recipient_manifests d join delivery_comparisons c on c.id=d.id join project_members m on m.project_id=d.project_id where m.member_id=$1 and d.project_id=$2 and c.edition=$3`, u.ID, r.PathValue("id"), edition).Scan(&status, &data)
	if err != nil {
		return err
	}
	w.Header().Set("X-Delivery-Distribution", status)
	if err = knowledge.DistributionError(status); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "attachment; filename=delivery.json")
	_, err = w.Write(data)
	return err
}

const affectedNodes = `with recursive edges as (select e.ordinal,e.depends_on from runs r join node_edges e on(e.project_id,e.plan_version)=(r.project_id,r.plan_version) where r.id=$1), affected(ordinal) as (select ordinal from edges where depends_on=$2 union select e.ordinal from edges e join affected a on e.depends_on=a.ordinal) `

func advanceDescendants(ctx context.Context, tx pgx.Tx, run string, ord int) error {
	// Only descendants of the changed node need a dependency review.
	if _, err := tx.Exec(ctx, affectedNodes+`insert into artifact_versions(artifact_id,version,body,reason) select a.id,a.current_version+1,v.body,$3 from artifacts a join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version) where a.run_id=$1 and a.ordinal in(select ordinal from affected)`, run, ord, "上游内容变化，需要核对引用"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, affectedNodes+`insert into artifact_files select a.id,a.current_version+1,f.object_hash,f.filename,f.media_type from artifacts a join artifact_files f on(f.artifact_id,f.version)=(a.id,a.current_version) where a.run_id=$1 and a.ordinal in(select ordinal from affected)`, run, ord); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, affectedNodes+"update artifacts set current_version=current_version+1 where run_id=$1 and ordinal in(select ordinal from affected)", run, ord); err != nil {
		return err
	}
	return nil
}
