package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"alliance/internal/bizconnectors"
	"alliance/internal/database"
	"alliance/internal/policy"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (d DingTalk) mutationMonitor(ctx context.Context) {
	if !d.configured() {
		return
	}
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_ = d.nextMutationNotice(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d DingTalk) nextMutationNotice(ctx context.Context) error {
	var job mutationTicket
	var revision int
	err := database.WithTx(ctx, d.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into business_mutation_notices(mutation_id,channel,binding_id,expected_revision)
		 select a.id,'dingtalk',i.id,a.revision from pending_system_mutations a join event_trigger_batches b on b.run_id=a.run_id
		  join event_triggers t on t.id=b.trigger_id join im_bindings i on i.connection=$1 and i.member_id=a.approver_id
		 where a.status='pending' and a.expires_at>statement_timestamp() and event_run_current(a.run_id)
		  and t.notify_targets @> jsonb_build_array(jsonb_build_object('channel','dingtalk','memberId',a.approver_id))
		 order by a.created_at limit 100 on conflict(mutation_id,channel) do update set binding_id=excluded.binding_id,
		  expected_revision=excluded.expected_revision,message_id=null,sent_at=null,attempts=0,next_at=clock_timestamp()
		  where (business_mutation_notices.binding_id,business_mutation_notices.expected_revision) is distinct from
		  (excluded.binding_id,excluded.expected_revision)`, d.key); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `with due as (
		 select n.mutation_id from business_mutation_notices n join pending_system_mutations a on a.id=n.mutation_id
		  join im_bindings i on (i.id,i.member_id,i.connection)=(n.binding_id,a.approver_id,$1)
		 where n.channel='dingtalk' and n.sent_at is null and n.attempts<4 and n.next_at<=clock_timestamp()
		  and a.status='pending' and a.revision=n.expected_revision and a.expires_at>clock_timestamp() and event_run_current(a.run_id)
		 order by n.next_at,n.mutation_id limit 1 for update of n skip locked
		), lease as (
		 update business_mutation_notices n set attempts=n.attempts+1,next_at=clock_timestamp()+interval '1 minute'
		 from due where n.mutation_id=due.mutation_id and n.channel='dingtalk' returning n.mutation_id,n.binding_id,n.expected_revision
		) select a.id,r.id,r.project_id,a.approver_id,l.binding_id,i.external_user,l.expected_revision
		 from lease l join pending_system_mutations a on a.id=l.mutation_id join runs r on r.id=a.run_id join im_bindings i on i.id=l.binding_id`, d.key).
			Scan(&job.ID, &job.Run, &job.Project, &job.Reviewer, &job.Binding, &job.Recipient, &revision)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	card, err := (bizconnectors.Server{DB: d.db, Vault: d.vault}).MutationNotice(ctx, job.ID, job.Reviewer)
	if err != nil || card.Revision != revision {
		return err
	}
	track := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s/%s/%d", job.ID, job.Binding, revision))).String()
	link := strings.TrimRight(d.public, "/") + "/chat/" + url.PathEscape(job.Project) + "#mutation-" + job.ID
	title := "业务事件写回待确认"
	values := []string{card.Label, "业务记录 " + card.Record}
	keys := make([]string, 0, len(card.After))
	for name := range card.After {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		values = append(values, fmt.Sprintf("%s　%v → %v", name, card.Before[name], card.After[name]))
	}
	guard := policy.ExternalDLP{DB: d.db, Vault: d.vault, Actor: job.Reviewer, Run: job.Run, Endpoint: dingAPIOrigin,
		Exchange: "business-approval/" + track, Classification: policy.SensitivityInternal}
	masked, err := guard.ProtectExternal(ctx, "business/approval", values)
	if err != nil && !errors.Is(err, policy.ErrDLPBlocked) {
		return err
	}
	all := strings.Join(values, "\n")
	canApprove := d.mutationTemplate != "" && strings.Join(masked, "\n") == all && len([]rune(all)) <= 1600
	var current bool
	err = d.db.QueryRow(ctx, `select exists(select from im_bindings i join business_mutation_notices n on n.binding_id=i.id
	 join pending_system_mutations a on a.id=n.mutation_id and a.approver_id=i.member_id
	 where (i.connection,i.id,i.external_user,a.id,a.revision)=($1,$2,$3,$4,$5) and n.channel='dingtalk'
	  and a.status='pending' and event_run_current(a.run_id) and space_role(a.space_id,i.member_id) in ('member','space_admin'))`,
		d.key, job.Binding, job.Recipient, job.ID, revision).Scan(&current)
	if err != nil || !current {
		return err
	}
	messageID := ""
	if canApprove {
		if err := d.sendMutationInstance(ctx, job.Recipient, track, title, all, link); err != nil {
			return err
		}
		messageID = track
	} else {
		outcome := d.privateActionCard(ctx, job.Recipient, title, "在团队工作区查看变更的原值与拟变更值并完成确认。", "核对变更", link)
		if outcome.state != "sent" {
			return nil
		}
	}
	_, err = d.db.Exec(ctx, `update business_mutation_notices set sent_at=clock_timestamp(),message_id=nullif($3,'')
	 where mutation_id=$1 and binding_id=$2 and channel='dingtalk' and expected_revision=$4`, job.ID, job.Binding, messageID, revision)
	return err
}

func (d DingTalk) sendMutationInstance(ctx context.Context, recipient, track, title, text, link string) error {
	// The tenant template binds title, content, url, and status. Its callback
	// button IDs are approve and reject.
	var auth struct{ AccessToken string }
	code, err := d.commentRequest(ctx, "/v1.0/oauth2/accessToken", "", map[string]string{"appKey": d.app, "appSecret": d.secret}, &auth)
	if err != nil || code != 200 || auth.AccessToken == "" {
		return errors.New("DingTalk card credential unavailable")
	}
	var result struct{ Success *bool }
	code, err = d.commentRequest(ctx, "/v1.0/card/instances/createAndDeliver", auth.AccessToken, map[string]any{
		"cardTemplateId": d.mutationTemplate, "outTrackId": track, "callbackType": "STREAM", "userIdType": 1,
		"openSpaceId": "dtv1.card//IM_ROBOT." + recipient, "imRobotOpenSpaceModel": map[string]bool{"supportForward": false},
		"imRobotOpenDeliverModel": map[string]string{"robotCode": d.robot, "spaceType": "IM_ROBOT"},
		"cardData":                map[string]any{"cardParamMap": map[string]string{"title": title, "content": text, "url": link, "status": "待确认"}},
	}, &result)
	if err != nil || code < 200 || code > 299 || result.Success != nil && !*result.Success {
		return errors.New("DingTalk approval card did not confirm delivery")
	}
	return nil
}

func (d DingTalk) mutationCallback(ctx context.Context, raw string) string {
	response := func(status string) string {
		data, _ := json.Marshal(map[string]any{"response": map[string]any{"cardUpdateOptions": map[string]bool{"updateCardDataByKey": true},
			"cardData": map[string]any{"cardParamMap": map[string]string{"status": status}}}})
		return string(data)
	}
	var payload struct {
		CorpID, UserID, OutTrackID, Content string
		UserIDType                          int
	}
	var action struct{ CardPrivateData struct{ ActionIDs []string } }
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.CorpID != d.corp || payload.UserIDType != 1 ||
		!dingOpaqueID.MatchString(payload.UserID) || !uuidPattern.MatchString(payload.OutTrackID) ||
		json.Unmarshal([]byte(payload.Content), &action) != nil || len(action.CardPrivateData.ActionIDs) != 1 {
		return `{"response":null}`
	}
	choice := action.CardPrivateData.ActionIDs[0]
	if choice != "approve" && choice != "reject" {
		return `{"response":null}`
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := database.WithTx(ctx, d.db, func(tx pgx.Tx) error {
		var mutation, actor string
		var expected int
		err := tx.QueryRow(ctx, `select n.mutation_id,i.member_id,n.expected_revision from business_mutation_notices n
		 join im_bindings i on i.id=n.binding_id where n.channel='dingtalk' and n.sent_at is not null and n.message_id=$1
		  and (i.connection,i.external_user)=($2,$3) for share of i`, payload.OutTrackID, d.key, payload.UserID).
			Scan(&mutation, &actor, &expected)
		if err != nil {
			return err
		}
		return bizconnectors.DecideMutation(ctx, tx, "", mutation, actor, "dingtalk", expected, choice == "approve")
	})
	var problem web.Problem
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && problem.Status < 500 {
		return response("确认状态或权限已变化")
	}
	if err != nil {
		return response("暂时无法处理，请打开工作区核对")
	}
	return response("已登记审批决定")
}
