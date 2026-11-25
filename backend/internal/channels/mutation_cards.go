package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"alliance/internal/bizconnectors"
	"alliance/internal/database"
	"alliance/internal/policy"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type mutationTicket struct {
	ID, Run, Project, Reviewer, Binding, Recipient string
}

func (f Feishu) MutationMonitor(ctx context.Context) {
	if !f.enabled() {
		return
	}
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for range 8 {
			busy, err := f.notifyMutation(ctx, f.sendMutationCard)
			if err != nil || !busy {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (f Feishu) notifyMutation(ctx context.Context, send func(context.Context, mutationTicket, string) (string, error)) (bool, error) {
	var job mutationTicket
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into business_mutation_notices(mutation_id,channel,binding_id)
		  select a.id,'feishu',l.binding_id from pending_system_mutations a join channel_links l on l.member_id=a.approver_id and l.tenant=$1
		   where a.status='pending' and a.expires_at>clock_timestamp() order by a.created_at limit 200
		  on conflict(mutation_id,channel) do update set binding_id=excluded.binding_id,message_id=null,sent_at=null,attempts=0,next_at=clock_timestamp()
		  where business_mutation_notices.binding_id<>excluded.binding_id`, f.Tenant); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `with due as (
		  select n.mutation_id from business_mutation_notices n join pending_system_mutations a on a.id=n.mutation_id
		   where n.channel='feishu' and n.sent_at is null and n.attempts<5 and n.next_at<=clock_timestamp()
		    and a.status='pending' and a.expires_at>clock_timestamp() and exists(select from channel_links l
		     where l.binding_id=n.binding_id and l.tenant=$1 and l.member_id=a.approver_id)
		   order by n.next_at,n.mutation_id for update of n skip locked limit 1
		 ), leased as (
		  update business_mutation_notices n set attempts=n.attempts+1,next_at=clock_timestamp()+interval '1 minute'
		  from due where n.mutation_id=due.mutation_id and n.channel='feishu' returning n.mutation_id,n.binding_id
		 ) select a.id,a.run_id,r.project_id,a.approver_id,l.binding_id,l.open_id from leased n
		 join pending_system_mutations a on a.id=n.mutation_id join runs r on r.id=a.run_id
		 join channel_links l on l.binding_id=n.binding_id and l.tenant=$1 and l.member_id=a.approver_id`, f.Tenant).
			Scan(&job.ID, &job.Run, &job.Project, &job.Reviewer, &job.Binding, &job.Recipient)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	business := bizconnectors.Server{DB: f.DB, Vault: f.Vault}
	proposal, err := business.MutationNotice(ctx, job.ID, job.Reviewer)
	if err != nil {
		return true, err
	}
	guard := policy.ExternalDLP{DB: f.DB, Vault: f.Vault, Actor: job.Reviewer, Run: job.Run,
		Exchange: "business-approval/" + job.ID, Endpoint: feishuOrigin, Classification: policy.SensitivityInternal}
	values := []string{proposal.Label, "业务记录 " + proposal.Record}
	keys := make([]string, 0, len(proposal.After))
	for field := range proposal.After {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		values = append(values, fmt.Sprintf("%s　%v → %v", field, proposal.Before[field], proposal.After[field]))
	}
	safe, err := guard.ProtectExternal(ctx, "business/approval", values)
	if err != nil && !errors.Is(err, policy.ErrDLPBlocked) {
		return true, err
	}
	title := "业务写回待确认"
	elements := []cardElement{}
	canDecide := len(safe) == len(values)
	for i, text := range safe {
		if i >= len(values) {
			break
		}
		if text != values[i] || clipped(text, 220) != text {
			canDecide = false
		}
		elements = append(elements, cardLine(clipped(text, 220)))
	}
	if canDecide {
		for _, action := range []struct {
			label  string
			accept bool
		}{{"确认写回", true}, {"拒绝", false}} {
			elements = append(elements, cardElement{"tag": "button", "text": plain(action.label),
				"confirm": cardElement{"title": plain(action.label), "text": plain("请核对卡片中的原值与拟变更值。")},
				"behaviors": []cardElement{{"type": "callback", "value": cardElement{
					"mutation": job.ID, "expected": proposal.Revision, "accept": action.accept,
				}}},
			})
		}
	}
	elements = append(elements, cardLink("在工作区查看完整变更", f.Public+"/chat/"+url.PathEscape(job.Project)+"#mutation-"+job.ID))
	content, err := cardPayload(title, elements)
	if err != nil {
		return true, err
	}
	receipt, err := send(ctx, job, content)
	if err != nil || receipt == "" {
		return true, err
	}
	_, err = f.DB.Exec(ctx, `update business_mutation_notices set sent_at=clock_timestamp(),message_id=$3
	 where (mutation_id,binding_id)=($1,$2) and channel='feishu'`, job.ID, job.Binding, receipt)
	return true, err
}

func (f Feishu) sendMutationCard(ctx context.Context, job mutationTicket, content string) (string, error) {
	token, err := f.accessToken(ctx)
	if err != nil {
		return "", err
	}
	var current bool
	err = f.DB.QueryRow(ctx, `select exists(select from channel_links l join pending_system_mutations a on a.approver_id=l.member_id
	 where (a.id,l.tenant,l.binding_id,l.open_id)=($1,$2,$3,$4) and a.status='pending' and a.expires_at>statement_timestamp()
	  and space_role(a.space_id,l.member_id) in ('member','space_admin'))`, job.ID, f.Tenant, job.Binding, job.Recipient).Scan(&current)
	if err != nil || !current {
		return "", err
	}
	response, err := feishuPost(ctx, "im/v1/messages?receive_id_type=open_id", token, map[string]string{
		"receive_id": job.Recipient, "msg_type": "interactive", "content": content, "uuid": job.ID,
	})
	if err != nil {
		return "", err
	}
	data, _ := response["data"].(map[string]any)
	message, _ := data["message_id"].(string)
	if message == "" || len(message) > 256 {
		return "", errFeishuUnconfirmed
	}
	return message, nil
}

func (f Feishu) mutationCardAction(w http.ResponseWriter, r *http.Request, event cardActionEvent) error {
	toast := func(kind, message string) error {
		return web.JSON(w, 200, cardElement{"toast": cardElement{"type": kind, "content": message}})
	}
	action := event.Action.Value
	if event.Host != "im_message" || event.Action.Tag != "button" || event.Operator.Tenant != f.Tenant ||
		event.Operator.Open == "" || event.Context.Message == "" || !uuidPattern.MatchString(action.Mutation) ||
		action.Adopt != "" || action.Accept == nil || action.Expected < 1 {
		return toast("warning", "这项写回确认已失效。")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var actor string
		err := tx.QueryRow(ctx, `select l.member_id from business_mutation_notices n join channel_links l on l.binding_id=n.binding_id
		 where n.mutation_id=$1 and n.channel='feishu' and n.sent_at is not null and n.message_id=$2 and l.tenant=$3 and l.open_id=$4
		 for share of l`, action.Mutation, event.Context.Message, f.Tenant, event.Operator.Open).Scan(&actor)
		if err != nil {
			return err
		}
		return bizconnectors.DecideMutation(ctx, tx, "", action.Mutation, actor, "feishu", action.Expected, *action.Accept)
	})
	var problem web.Problem
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && problem.Status < 500 {
		return toast("warning", "确认状态或权限已变化，请在工作区核对。")
	}
	if err != nil {
		return toast("error", "暂时无法处理这项确认。")
	}
	return toast("success", "已登记审批决定，业务写回以正式回执为准。")
}
