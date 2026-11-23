package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"alliance/internal/database"
	"alliance/internal/projects"
	"alliance/internal/services"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

var fileIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,160}$`)

func (f Feishu) stageFile(ctx context.Context, tx pgx.Tx, actor string, msg channelMessage) (channelReply, error) {
	var file struct {
		Name string `json:"file_name"`
		Key  string `json:"file_key"`
	}
	created, parseErr := strconv.ParseInt(msg.Created, 10, 64)
	if parseErr != nil || created <= 0 || !fileIdentifier.MatchString(msg.ID) ||
		json.Unmarshal([]byte(msg.Content), &file) != nil || !fileIdentifier.MatchString(file.Key) {
		return channelReply{note: "这条资料消息无法识别，请重新发送文件。"}, nil
	}
	job := attachmentJob{actor: actor, name: file.Name}
	err := tx.QueryRow(ctx, `select id,`+attachmentTargetFields+` from channel_proposals
 where tenant=$1 and chat_id=$2 and member_id=$3 and expires_at>clock_timestamp()
 and origin_time>0 and origin_time<=$4 and file_time<$4
 and (($5='' and $6='') or origin_message_id in ($5,$6))`+receiptContextSQL+` for update`,
		f.Tenant, msg.Chat, actor, created, msg.Parent, msg.Root).Scan(append([]any{&job.id}, job.targetFields()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "这份资料未关联当前服务预览。请查看最新预览，或发送“进度”查看是否待补件。"}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	if err := job.checkScope(ctx, tx); err != nil {
		return attachmentReply(err)
	}
	var next string
	err = tx.QueryRow(ctx, `update channel_proposals set id=gen_random_uuid(),file_message_id=$2,file_key=$3,file_name=$4,file_time=$5
 where id=$1 returning id`, job.id, msg.ID, file.Key, file.Name, created).Scan(&next)
	kind := "proposal"
	if job.receipt.run != "" {
		kind = "receipt_proposal"
	}
	return channelReply{kind: kind, argument: next}, err
}

type attachmentJob struct {
	id, event, actor, binding, chat, service, input, message, key, name string
	version, calls, attempts                                            int
	accepted, expires                                                   time.Time
	wait                                                                services.SourceWait
	receipt                                                             runReceipt
}

const attachmentTargetFields = `coalesce(service_id::text,''),coalesce(version,0),coalesce(calls,0),input,
 await_lookup_receipt,await_calculation_receipt,
 coalesce(receipt_run::text,''),coalesce(receipt_step,0),coalesce(receipt_revision,0),coalesce(receipt_kind,'')`

func (j *attachmentJob) targetFields() []any {
	return []any{&j.service, &j.version, &j.calls, &j.input, &j.wait.Lookup, &j.wait.Calculation,
		&j.receipt.run, &j.receipt.step, &j.receipt.revision, &j.receipt.kind}
}

func (f Feishu) confirmProposal(ctx context.Context, tx pgx.Tx, actor, binding, event, chat, id string) (channelReply, error) {
	if !uuidPattern.MatchString(id) {
		return channelReply{note: "请复制当前预览中的完整确认指令。"}, nil
	}
	job := attachmentJob{id: id, actor: actor, event: event, binding: binding, chat: chat}
	err := tx.QueryRow(ctx, `delete from channel_proposals where id=$1 and tenant=$2 and chat_id=$3 and member_id=$4 and expires_at>now()
 and (receipt_run is null or file_key<>'')`+receiptContextSQL+`
 returning file_message_id,file_key,file_name,expires_at,`+attachmentTargetFields, id, f.Tenant, chat, actor).
		Scan(append([]any{&job.message, &job.key, &job.name, &job.expires}, job.targetFields()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "确认指令已失效或已处理，可发送“进度”或查看最新预览。"}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	if job.key == "" {
		reply, err := job.apply(ctx, tx, nil)
		if err != nil || reply.project == "" {
			return reply, err
		}
		return reply, f.setProject(ctx, tx, chat, actor, reply.project)
	}
	if err := job.checkScope(ctx, tx); err != nil {
		return attachmentReply(err)
	}
	_, err = tx.Exec(ctx, `insert into channel_attachment_jobs(id,event_id,tenant,chat_id,member_id,binding_id,
 service_id,version,calls,input,file_message_id,file_key,file_name,expires_at,await_lookup_receipt,await_calculation_receipt,
 receipt_run,receipt_step,receipt_revision,receipt_kind)
 values($1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,nullif($8,0),nullif($9,0),$10,$11,$12,$13,$14,$15,$16,
 nullif($17,'')::uuid,nullif($18,0),case when $17<>'' then $19::integer end,nullif($20,''))`,
		id, event, f.Tenant, chat, actor, binding, job.service, job.version, job.calls, job.input, job.message, job.key,
		job.name, job.expires, job.wait.Lookup, job.wait.Calculation, job.receipt.run, job.receipt.step, job.receipt.revision, job.receipt.kind)
	return channelReply{kind: "attachment_pending"}, err
}

func attachmentReply(err error) (channelReply, error) {
	if note := attachmentProblem(err); note != "" {
		return channelReply{note: note}, nil
	}
	return channelReply{}, err
}

func (j attachmentJob) checkScope(ctx context.Context, tx pgx.Tx) error {
	if j.receipt.run == "" {
		return services.CheckMemberAttachment(ctx, tx, j.actor, j.service, j.name, j.version, j.calls)
	}
	if err := services.ValidateAttachmentName(j.name); err != nil {
		return err
	}
	_, err := j.receipt.currentProject(ctx, tx, j.actor)
	return err
}

func attachmentProblem(err error) string {
	if errors.Is(err, pgx.ErrNoRows) {
		return "服务或授权已变化，请重新查询并发起。"
	}
	var problem web.Problem
	if errors.As(err, &problem) && problem.Status < 500 {
		return problem.Message + "。可发送“进度”核对当前任务或转到企业网页处理。"
	}
	return ""
}

func (j attachmentJob) apply(ctx context.Context, tx pgx.Tx, file *services.Attachment) (channelReply, error) {
	var reply channelReply
	err := pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
		if j.receipt.run == "" {
			result, err := services.LaunchMember(ctx, inner, j.actor, j.service, j.input, "feishu/"+j.id, j.version, j.calls, file, j.wait)
			reply = channelReply{project: result["project"], note: "服务调用已受理，可在本对话查询进度。"}
			return err
		}
		project, err := j.receipt.currentProject(ctx, inner, j.actor)
		if err != nil {
			return err
		}
		if err := file.Validate(); err != nil {
			return err
		}
		_, err = projects.ApplySourceReceipt(ctx, inner, j.receipt.run, j.actor, "feishu/receipt/"+j.id, j.receipt.kind,
			projects.ReceiptSelection{Step: j.receipt.step, Expected: j.receipt.revision, Excerpt: j.input},
			&projects.ReceiptFile{Name: file.Name, Text: string(file.Data)})
		reply = channelReply{project: project, note: "补件已登记，原任务继续执行。"}
		return err
	})
	if err != nil {
		return attachmentReply(err)
	}
	return reply, nil
}

var errAttachmentBinding = errors.New("Feishu attachment binding changed")

func (f Feishu) checkAttachmentBinding(ctx context.Context, tx pgx.Tx, job attachmentJob) error {
	var linked bool
	err := tx.QueryRow(ctx, `select exists(select from channel_links l join members m on m.id=l.member_id and m.enabled
 where l.tenant=$1 and l.member_id=$2 and l.binding_id=$3 for share of l,m)`, f.Tenant, job.actor, job.binding).Scan(&linked)
	if err == nil && !linked {
		return errAttachmentBinding
	}
	return err
}

func (f Feishu) checkAttachmentJob(ctx context.Context, job attachmentJob) error {
	return database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var readable bool
		if err := tx.QueryRow(ctx, `select exists(select from channel_attachment_jobs
 where id=$1 and attempts=$2 and expires_at>clock_timestamp())`, job.id, job.attempts).Scan(&readable); err != nil {
			return err
		}
		if !readable {
			return web.Fail(409, "读取飞书资料的确认已到期")
		}
		if err := f.checkAttachmentBinding(ctx, tx, job); err != nil {
			return err
		}
		return job.checkScope(ctx, tx)
	})
}

const maxAttachmentReads = 3

// ImportAttachments leases signed and confirmed work separately from outgoing replies.
// A retry reads the same origin; the launch and the completed reply commit together.
func (f Feishu) ImportAttachments(ctx context.Context) {
	if !f.enabled() {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := f.importAttachment(ctx); err != nil && ctx.Err() == nil {
				log.Printf("Feishu attachment queue: %T", err)
			}
		}
	}
}

func (f Feishu) importAttachment(ctx context.Context) error {
	var job attachmentJob
	// The lease exceeds the two HTTP timeouts. A finishing worker checks its
	// attempt number so a superseded lease cannot start a service.
	err := f.DB.QueryRow(ctx, `with due as (
 select id from channel_attachment_jobs where tenant=$1 and next_at<=clock_timestamp()
 order by next_at,id limit 1 for update skip locked
 )
 update channel_attachment_jobs j set attempts=j.attempts+1,next_at=clock_timestamp()+interval '25 seconds'
 from due where j.id=due.id
 returning j.id,j.event_id,j.member_id,j.binding_id,j.chat_id,j.file_message_id,j.file_key,j.file_name,
 j.attempts,j.accepted_at,j.expires_at,`+attachmentTargetFields, f.Tenant).
		Scan(append([]any{&job.id, &job.event, &job.actor, &job.binding, &job.chat, &job.message, &job.key, &job.name,
			&job.attempts, &job.accepted, &job.expires}, job.targetFields()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.attempts > maxAttachmentReads {
		return f.finishAttachment(ctx, job, nil, "暂时无法从飞书读取这份资料。可发送“进度”核对当前任务，或转到企业网页附资料。")
	}
	attachment, err := f.readAttachment(ctx, job)
	if err == nil {
		return f.finishAttachment(ctx, job, attachment, "")
	}
	if errors.Is(err, errAttachmentBinding) {
		return f.finishAttachment(ctx, job, nil, "飞书绑定已变化，请重新发起。")
	}
	if note := attachmentProblem(err); note != "" {
		return f.finishAttachment(ctx, job, nil, note)
	}
	if job.attempts >= maxAttachmentReads {
		return f.finishAttachment(ctx, job, nil, "暂时无法从飞书读取这份资料。可发送“进度”核对当前任务，或转到企业网页附资料。")
	}
	_, err = f.DB.Exec(ctx, `update channel_attachment_jobs set next_at=clock_timestamp()+make_interval(secs=>$3)
 where id=$1 and attempts=$2`, job.id, job.attempts, float64(2*job.attempts))
	return err
}

func (f Feishu) readAttachment(ctx context.Context, job attachmentJob) (*services.Attachment, error) {
	if err := f.checkAttachmentJob(ctx, job); err != nil {
		return nil, err
	}
	token, err := f.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	if err := f.checkAttachmentJob(ctx, job); err != nil {
		return nil, err
	}
	if !fileIdentifier.MatchString(job.message) || !fileIdentifier.MatchString(job.key) {
		return nil, web.Fail(422, "飞书资料标识无效")
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(job.message) + "/resources/" + url.PathEscape(job.key) + "?type=file"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuOrigin+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := feishuClient()
	rsp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode == http.StatusTooManyRequests || rsp.StatusCode >= 500 {
		return nil, errors.New("Feishu file service temporarily unavailable")
	}
	disposition, _, _ := mime.ParseMediaType(rsp.Header.Get("Content-Disposition"))
	mediaType, _, _ := mime.ParseMediaType(rsp.Header.Get("Content-Type"))
	if rsp.StatusCode != http.StatusOK || (disposition != "attachment" && mediaType != "application/octet-stream") {
		return nil, web.Fail(422, "飞书未返回可读取的原件，可改用企业网页附资料")
	}
	raw, err := io.ReadAll(io.LimitReader(rsp.Body, services.AttachmentLimit+1))
	if err != nil {
		return nil, err
	}
	return &services.Attachment{Name: job.name, Data: raw}, nil
}

func (f Feishu) finishAttachment(ctx context.Context, job attachmentJob, file *services.Attachment, note string) error {
	return database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var valid bool
		err := tx.QueryRow(ctx, `select expires_at>clock_timestamp() from channel_attachment_jobs
 where id=$1 and attempts=$2 for update`, job.id, job.attempts).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		reply := channelReply{note: note}
		bindingErr := f.checkAttachmentBinding(ctx, tx, job)
		switch {
		case errors.Is(bindingErr, errAttachmentBinding):
			_, err = tx.Exec(ctx, "update channel_replies set suppressed_at=now(),reply_kind='note' where event_id=$1", job.event)
		case bindingErr != nil:
			return bindingErr
		default:
			switch {
			case !valid:
				reply.note = "读取飞书资料的确认已到期，请回到对应任务或服务重新确认资料。"
			case file != nil:
				reply, err = job.apply(ctx, tx, file)
				if err != nil {
					return err
				}
			}
			if reply.project != "" && job.receipt.run == "" {
				tag, err := tx.Exec(ctx, `insert into channel_contexts(tenant,chat_id,member_id,project_id,changed_at)
 values($1,$2,$3,$4,$5) on conflict(tenant,chat_id,member_id) do update
 set project_id=excluded.project_id,changed_at=excluded.changed_at
 where channel_contexts.changed_at<excluded.changed_at`,
					f.Tenant, job.chat, job.actor, reply.project, job.accepted)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					reply.note = "服务调用已受理。打开项目链接查看，或在单聊发送“切换 " + reply.project + "”。"
				}
			}
			_, err = tx.Exec(ctx, `update channel_replies set reply_kind='note',note=$2,project_id=nullif($3,'')::uuid,next_at=now()
 where event_id=$1`, job.event, reply.note, reply.project)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "delete from channel_attachment_jobs where id=$1", job.id)
		return err
	})
}
