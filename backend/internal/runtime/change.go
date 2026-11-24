package runtime

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Read current permissions at dispatch and result publication. Input content is pinned
// to the authorized versions so a concurrent human edit can be compared afterwards.
func changeContext(ctx context.Context, tx pgx.Tx, run string, d *contextData) (bool, bool, error) {
	var permitted, current bool
	var position, instruction, base, prior, replacement string
	err := tx.QueryRow(ctx, `select p.stage='review' and f.state='required' and c.disposition='pending' and a.current_version=c.base_version and f.revision=c.feedback_revision and not exists(
 select from change_inputs i join artifacts dep on dep.id=i.artifact_id where i.run_id=$1 and dep.current_version<>i.version)
 and not exists(select from (select artifact_id,version from change_inputs where run_id=$1 union select c.artifact_id,c.base_version) input
 cross join lateral working_artifact_origin(input.artifact_id,input.version) origin
 where not run_working_set_usable(origin.run_id,origin.ordinal,origin.attempt_no)),
 exists(select from project_members pm join members m on m.id=pm.member_id where pm.project_id=p.id and m.enabled and pm.member_id=$2 and (pm.can_review or p.owner=$2)),
 f.position,f.body,v.body,c.replacement,c.excerpt,c.artifact_id,c.base_version,c.image_region,coalesce((select string_agg(dep.name||' v'||i.version||E'\n'||dv.body,E'\n\n' order by dep.ordinal)
 from change_inputs i join artifacts dep on dep.id=i.artifact_id join artifact_versions dv on(dv.artifact_id,dv.version)=(i.artifact_id,i.version) where i.run_id=$1),'')
 from change_batches c join feedback f on f.id=c.feedback_id join artifact_versions v on(v.artifact_id,v.version)=(c.artifact_id,c.base_version)
 join artifacts a on a.id=c.artifact_id join projects p on p.id=a.project_id where c.run_id=$1`, run, d.actor).Scan(&current, &permitted, &position, &instruction, &base, &replacement, &d.excerpt, &d.changeArtifact, &d.changeBaseVersion, &d.imageRegion, &prior)
	if err != nil {
		return false, false, err
	}
	if !permitted || !current {
		return permitted, current, nil
	}
	d.brief += fmt.Sprintf("\n请仅修改下面这份成果，返回完整修订稿。\n基准稿：\n%s\n反馈位置：%s\n修改要求：%s", base, position, instruction)
	if d.excerpt != "" {
		d.baseBody = base
		d.brief += fmt.Sprintf("\n修改范围已固定。仅返回以下原文片段的替换文本，平台负责保留其余部分：\n%s", d.excerpt)
	}
	d.prior = prior
	d.replacement = replacement
	return permitted, current, nil
}
