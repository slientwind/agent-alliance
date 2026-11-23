package projects

import (
	"context"

	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func checkPlanPictures(ctx context.Context, tx pgx.Tx, project string, version int, starting bool) error {
	var usesPictures, editsPicture, hasLookup, hasText, unavailable bool
	var count int
	var size int64
	err := tx.QueryRow(ctx, `select
 exists(select from nodes n join agent_revisions a on (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
        where (n.project_id,n.plan_version)=($1,$2) and agent_uses_picture(a)),
 exists(select from nodes n join agent_revisions a on (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
        where (n.project_id,n.plan_version)=($1,$2) and agent_edits_picture(a)),
 exists(select from nodes n where (n.project_id,n.plan_version)=($1,$2) and n.lookup_phase='search'),
 coalesce(bool_or(s.revoked_at is null and v.extracted is not null),false),
 count(*) filter (where source_is_picture(v.media_type)),
 coalesce(sum(o.size_bytes) filter (where source_is_picture(v.media_type)),0),
 coalesce(bool_or(source_is_picture(v.media_type) and (s.revoked_at is not null or o.hash is null)),false)
 from plan_sources ps join sources s on s.id=ps.source_id
 join source_versions v on (v.source_id,v.version)=(ps.source_id,ps.source_version)
 left join objects o on o.hash=v.object_hash
 where (ps.project_id,ps.plan_version)=($1,$2)`, project, version).
		Scan(&usesPictures, &editsPicture, &hasLookup, &hasText, &count, &size, &unavailable)
	if err != nil {
		return err
	}
	if count > 0 && !usesPictures {
		return web.Fail(422, "方案已引用图片，请选用识图或参考图编辑 Agent，或取消引用图片")
	}
	if editsPicture && count > 1 {
		return web.Fail(422, "参考图编辑的方案只接受一张参考图")
	}
	if count > 0 && hasLookup && !hasText {
		return web.Fail(422, "原文核对需要另外引用至少一份可读取的文本资料")
	}
	if unavailable {
		return web.Fail(409, "已引用的图片不可读取，请核对固定版本及当前授权")
	}
	if count > knowledge.MaxPictures || size > knowledge.RunPictureLimit {
		return web.Fail(422, "视觉理解最多引用 4 张图片，合计不超过 6 MiB")
	}
	if starting && usesPictures && count == 0 {
		return web.Fail(409, "请引用当前可用的 PNG 或 JPEG 图片再执行")
	}
	return nil
}
