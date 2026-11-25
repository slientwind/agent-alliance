package channels

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type knowledgeTask struct {
	id, lease, space, gap, topic, recipient, binding string
}

func (f Feishu) KnowledgeTaskMonitor(ctx context.Context) {
	if !f.enabled() {
		return
	}
	tick := time.NewTicker(4 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := f.dispatchKnowledgeTask(ctx, f.createKnowledgeTask); err != nil && ctx.Err() == nil {
				slog.Warn("knowledge gap task delivery deferred")
			}
		}
	}
}

func (f Feishu) dispatchKnowledgeTask(ctx context.Context, send func(context.Context, knowledgeTask) (string, error)) error {
	if _, err := f.DB.Exec(ctx, `update kb_gap_task_outbox set status='failed',lease=null
	 where status='sending' and attempts>=6 and next_attempt_at<=clock_timestamp()`); err != nil {
		return err
	}
	var job knowledgeTask
	err := f.DB.QueryRow(ctx, `with due as (
	 select o.id,g.space_id,g.id gap,g.topic_summary,l.open_id,l.binding_id from kb_gap_task_outbox o
	 join kb_gap_clusters g on (g.id,g.revision,g.assignee_id)=(o.cluster_id,o.assignment_revision,o.assignee_id) and g.status='assigned'
	 join channel_links l on l.tenant=$1 and l.member_id=o.assignee_id
	 where o.status in ('pending','sending') and o.attempts<6 and o.next_attempt_at<=clock_timestamp()
	 and space_role(g.space_id,o.assignee_id) in ('space_admin','member')
	 order by o.next_attempt_at,o.id limit 1 for update of o skip locked
	) update kb_gap_task_outbox o set status='sending',lease=gen_random_uuid(),attempts=attempts+1,next_attempt_at=clock_timestamp()+interval '1 minute'
	 from due d where o.id=d.id returning o.id,o.lease,d.space_id,d.gap,d.topic_summary,d.open_id,d.binding_id`, f.Tenant).
		Scan(&job.id, &job.lease, &job.space, &job.gap, &job.topic, &job.recipient, &job.binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var current bool
	err = f.DB.QueryRow(ctx, `select exists(select from kb_gap_task_outbox o
	 join kb_gap_clusters g on (g.id,g.revision,g.assignee_id)=(o.cluster_id,o.assignment_revision,o.assignee_id) and g.status='assigned'
	 join channel_links l on l.tenant=$3 and l.member_id=o.assignee_id and l.binding_id=$4 and l.open_id=$5
	 where (o.id,o.lease)=($1,$2) and space_role(g.space_id,o.assignee_id) in ('space_admin','member'))`,
		job.id, job.lease, f.Tenant, job.binding, job.recipient).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		_, err := f.DB.Exec(ctx, "update kb_gap_task_outbox set status='cancelled',lease=null where (id,lease)=($1,$2)", job.id, job.lease)
		return err
	}
	request, stop := context.WithTimeout(ctx, 18*time.Second)
	task, callErr := send(request, job)
	stop()
	result, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()
	if callErr == nil && task != "" {
		_, err := f.DB.Exec(result, "update kb_gap_task_outbox set status='sent',task_guid=$3,lease=null where (id,lease)=($1,$2)", job.id, job.lease, task)
		return err
	}
	_, err = f.DB.Exec(result, `update kb_gap_task_outbox set status=case when attempts>=6 then 'failed' else 'pending' end,lease=null,
	 next_attempt_at=clock_timestamp()+make_interval(secs=>15*attempts) where (id,lease)=($1,$2)`, job.id, job.lease)
	return err
}

func (f Feishu) createKnowledgeTask(ctx context.Context, job knowledgeTask) (string, error) {
	token, err := f.accessToken(ctx)
	if err != nil {
		return "", err
	}
	description := "请在知识与数据工作台核对缺口并补齐对应的制度或 SOP。"
	if base, err := url.Parse(f.Public); err == nil && base.Scheme == "https" && base.Hostname() != "" && base.User == nil && base.RawQuery == "" && base.Fragment == "" {
		description += "\n" + strings.TrimRight(f.Public, "/") + "/knowledge?" + (url.Values{"space": {job.space}, "gap": {job.gap}}).Encode()
	}
	value, err := feishuPost(ctx, "task/v2/tasks?user_id_type=open_id", token, map[string]any{
		"summary": "补齐知识缺口 · " + job.topic, "description": description, "client_token": job.id,
		"members": []map[string]string{{"id": job.recipient, "type": "user", "role": "assignee"}},
	})
	if err != nil {
		return "", err
	}
	data, _ := value["data"].(map[string]any)
	task, _ := data["task"].(map[string]any)
	guid, _ := task["guid"].(string)
	if _, err := uuid.Parse(guid); err != nil {
		return "", errFeishuUnconfirmed
	}
	return guid, nil
}
