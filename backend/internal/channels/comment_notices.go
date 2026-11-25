package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const commentNoticeTitle = "成果评论提到了你"
const commentNoticeBody = "登录企业工作区查看评论及其在成果中的位置。"

func (f Feishu) CommentConnection() string {
	if f.enabled() {
		return f.Tenant
	}
	return ""
}

func (d DingTalk) CommentConnection() string {
	if d.configured() && commentLink(d.public, "p", "c") != "" {
		return d.key
	}
	return ""
}

type commentDelivery struct {
	id, comment, project, member, channel, connection, binding, lease string
	attempt                                                           int
}

type commentResult struct{ state, receipt string }

type commentNoticeSender func(context.Context, string, string, string) commentResult

func ServeCommentNotices(ctx context.Context, db *pgxpool.Pool, feishu Feishu, ding DingTalk) {
	feishuConnection, dingConnection := feishu.CommentConnection(), ding.CommentConnection()
	if feishuConnection == "" && dingConnection == "" {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := deliverCommentNotice(ctx, db, feishuConnection, dingConnection, feishu.Public, ding.public, feishu.commentNotice, ding.commentNotice); err != nil && ctx.Err() == nil {
			slog.Warn("comment notice delivery pending")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func deliverCommentNotice(ctx context.Context, db *pgxpool.Pool, feishuConnection, dingConnection, feishuBase, dingBase string, feishu, ding commentNoticeSender) error {
	var job commentDelivery
	err := db.QueryRow(ctx, `with expired as (
	  update artifact_comment_deliveries set state='unknown',lease=null where state='sending' and next_at<=clock_timestamp()
	 ), next as (
	  select d.id,a.project_id from artifact_comment_deliveries d join artifact_comments c on c.id=d.comment_id
	  join artifacts a on a.id=c.artifact_id
	  where state='pending' and next_at<=clock_timestamp() and ((channel='feishu' and connection=$1) or (channel='dingtalk' and connection=$2))
	  order by next_at,d.id for update of d skip locked limit 1
	 ) update artifact_comment_deliveries d set state='sending',lease=gen_random_uuid(),attempts=attempts+1,next_at=clock_timestamp()+interval '1 minute'
	 from next where next.id=d.id returning d.id,d.comment_id,next.project_id,d.member_id,d.channel,d.connection,d.binding,d.lease,d.attempts`,
		feishuConnection, dingConnection).Scan(&job.id, &job.comment, &job.project, &job.member, &job.channel, &job.connection, &job.binding, &job.lease, &job.attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	send, base := feishu, feishuBase
	if job.channel == "dingtalk" {
		send, base = ding, dingBase
	}
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		link := commentLink(base, job.project, job.comment)
		var target string
		if link != "" {
			err := tx.QueryRow(ctx, `select coalesce(f.open_id,d.external_user,'') from artifact_comment_deliveries j
			 join artifact_comments c on c.id=j.comment_id and c.resolved_at is null
			 join artifacts a on a.id=c.artifact_id join members m on m.id=j.member_id and m.enabled
			 join project_members pm on (pm.project_id,pm.member_id)=(a.project_id,m.id)
			 left join channel_links f on j.channel='feishu' and (f.tenant,f.binding_id,f.member_id)=(j.connection,j.binding,j.member_id)
			 left join im_bindings d on j.channel='dingtalk' and (d.connection,d.id,d.member_id)=(j.connection,j.binding,j.member_id)
			 where j.id=$1 and j.lease=$2 and j.state='sending' and artifact_comment_readable(c.id,m.id)
			 for share of c,a,m,pm`, job.id, job.lease).Scan(&target)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
		}
		result := commentResult{state: "revoked"}
		if target != "" {
			// Lock the existing binding through the short send. A reconnect using a
			// different account cannot change the destination of a queued notice.
			query := "select 1 from channel_links where (tenant,binding_id,member_id)=($1,$2,$3) for share"
			if job.channel == "dingtalk" {
				query = "select 1 from im_bindings where (connection,id,member_id)=($1,$2,$3) for share"
			}
			err := tx.QueryRow(ctx, query, job.connection, job.binding, job.member).Scan(new(int))
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			if err == nil {
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				result = send(call, target, link, job.id)
				cancel()
			}
		}
		if result.state == "pending" && job.attempt >= 4 {
			result.state = "failed"
		}
		_, err := tx.Exec(ctx, `update artifact_comment_deliveries set state=$3,receipt=nullif($4,''),lease=null,
		 next_at=clock_timestamp()+make_interval(secs=>$5) where id=$1 and lease=$2`, job.id, job.lease, result.state, result.receipt, 5<<min(4, job.attempt))
		return err
	})
}

func commentLink(base, project, comment string) string {
	root, err := url.Parse(base)
	if err != nil || root.Scheme != "https" || root.Host == "" || root.User != nil || root.RawQuery != "" || root.Fragment != "" {
		return ""
	}
	path, err := url.JoinPath(base, "chat", project)
	if err != nil {
		return ""
	}
	return path + "?task=" + url.QueryEscape("mention-"+comment)
}

func (f Feishu) commentNotice(ctx context.Context, recipient, link, id string) commentResult {
	token, err := f.accessToken(ctx)
	if err != nil {
		return commentResult{state: "pending"}
	}
	content, err := cardPayload(commentNoticeTitle, []cardElement{cardLine(commentNoticeBody), cardLink("查看评论", link)})
	if err != nil {
		return commentResult{state: "failed"}
	}
	resp, err := feishuPost(ctx, "im/v1/messages?receive_id_type=open_id", token, map[string]string{
		"receive_id": recipient, "uuid": id, "msg_type": "interactive", "content": content,
	})
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) || errors.Is(err, errFeishuUnconfirmed) {
			return commentResult{state: "pending"}
		}
		return commentResult{state: "failed"}
	}
	data, _ := resp["data"].(map[string]any)
	receipt, _ := data["message_id"].(string)
	if receipt == "" || len(receipt) > 256 {
		return commentResult{state: "unknown"}
	}
	return commentResult{"sent", receipt}
}

const dingAPIOrigin = "https://api.dingtalk.com"

func (d DingTalk) commentNotice(ctx context.Context, recipient, link, _ string) commentResult {
	var credentials struct{ AccessToken string }
	status, err := d.commentRequest(ctx, "/v1.0/oauth2/accessToken", "", map[string]string{"appKey": d.app, "appSecret": d.secret}, &credentials)
	if err != nil || status != 200 || credentials.AccessToken == "" {
		return commentResult{state: "pending"}
	}
	card, _ := json.Marshal(map[string]string{"title": commentNoticeTitle, "text": commentNoticeBody, "singleTitle": "查看评论", "singleURL": link})
	var receipt struct {
		ProcessQueryKey           string
		InvalidStaffIDList        []string `json:"invalidStaffIdList"`
		FlowControlledStaffIDList []string `json:"flowControlledStaffIdList"`
	}
	status, err = d.commentRequest(ctx, "/v1.0/robot/oToMessages/batchSend", credentials.AccessToken, map[string]any{
		"robotCode": d.robot, "userIds": []string{recipient}, "msgKey": "sampleActionCard", "msgParam": string(card),
	}, &receipt)
	switch {
	case status == 429 || len(receipt.FlowControlledStaffIDList) != 0:
		return commentResult{state: "pending"}
	case status >= 400 && status < 500 || len(receipt.InvalidStaffIDList) != 0:
		return commentResult{state: "failed"}
	case err != nil || status != 200 || receipt.ProcessQueryKey == "" || len(receipt.ProcessQueryKey) > 256:
		return commentResult{state: "unknown"}
	default:
		return commentResult{"sent", receipt.ProcessQueryKey}
	}
}

func (d DingTalk) commentRequest(ctx context.Context, path, token string, payload, result any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dingAPIOrigin+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("x-acs-dingtalk-access-token", token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(result)
}
