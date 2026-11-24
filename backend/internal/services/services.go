package services

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/planner"
	"alliance/internal/projects"
	"alliance/internal/web"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type Server struct {
	DB      *pgxpool.Pool
	Planner planner.Server
}

//go:embed openapi.json
var serviceContract json.RawMessage

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	for p, h := range map[string]identity.Action{"GET /api/services": s.list, "GET /api/services/{id}/access": s.access, "GET /api/services/{id}/entry": s.entry, "POST /api/services/{id}/delegate": s.delegate, "DELETE /api/delegations/{id}": s.revokeDelegation, "POST /api/services/{id}/chat": s.chat} {
		m.Handle(p, g.Protect(false, h))
	}
	for p, h := range map[string]identity.Action{"POST /api/services": s.publish, "GET /api/services/{id}/versions": s.serviceVersions, "GET /api/services/{id}/versions/{version}/preflight": s.rollbackPreview, "POST /api/services/{id}/rollback": s.rollback, "POST /api/services/{id}/applications": s.issue, "DELETE /api/applications/{id}": s.revokeApp} {
		m.Handle(p, g.Protect(true, h))
	}
	m.Handle("GET /api/v1/openapi.json", web.Handler(func(w http.ResponseWriter, _ *http.Request) error { return web.JSON(w, 200, serviceContract) }))
	m.Handle("GET /api/v1/services/{id}", web.Handler(s.describe))
	m.Handle("GET /api/v1/runs/{id}/events", web.Handler(s.events))
	m.Handle("GET /api/v1/runs/{id}/rule-rebuild", web.Handler(s.previewRuleBranch))
	m.Handle("POST /api/v1/runs/{id}/actions", web.Handler(s.action))
	m.Handle("POST /api/v1/runs/{id}/calls/{step}/column-suggestion", web.Handler(s.suggestColumns))
	m.Handle("POST /api/v1/services/{id}/runs", web.Handler(s.start))
	m.Handle("GET /api/v1/runs/{id}", web.Handler(s.progress))
	m.Handle("GET /api/v1/runs/{id}/delivery/package", web.Handler(s.deliveryPackage))
	m.Handle("GET /api/v1/runs/{id}/delivery", web.Handler(s.delivery))
}
func (s Server) list(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select s.id,s.name,s.current_version,s.routing_revision,sv.instructions,sv.example_input,count(n.ordinal)::integer from services s join team_members t on t.team_id=s.team_id join service_versions sv on(sv.service_id,sv.version)=(s.id,s.current_version) join service_nodes n on(n.service_id,n.version)=(s.id,s.current_version) where t.member_id=$1 and enabled group by s.id,sv.instructions,sv.example_input order by s.name`, u.ID)
	if err != nil {
		return err
	}
	type item struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Version         int    `json:"version"`
		RoutingRevision int    `json:"routingRevision"`
		Brief           string `json:"brief"`
		Example         string `json:"example"`
		Calls           int    `json:"calls"`
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[item])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
func (s Server) publish(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		ID, Name, Agent, Project, Instructions, Example string
		Expected, ExpectedRoutingRevision               int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if len(in.Example) > 4000 {
		return web.Fail(422, "使用示例过长")
	}
	if web.Blank(in.Name) || web.Blank(in.Instructions) || (in.Agent == "") == (in.Project == "") {
		return web.Fail(422, "请选择一个 Agent 或一份已确认方案")
	}
	ctx := r.Context()
	id := in.ID
	version, routing := 1, 1
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var team string
		sourceID := in.Agent
		var scope string
		if in.Agent != "" {
			scope = "select a.team_id from agents a join team_members tm on tm.team_id=a.team_id and tm.member_id=$2 where a.id=$1 for share of a,tm"
		} else {
			sourceID = in.Project
			scope = "select p.team_id from projects p join team_members tm on tm.team_id=p.team_id and tm.member_id=$2 where p.id=$1 and p.owner=$2 for share of p,tm"
		}
		if err := tx.QueryRow(ctx, scope, sourceID, u.ID).Scan(&team); err != nil {
			if err == pgx.ErrNoRows {
				return web.Fail(409, "来源未发布、尚未确认或无权复用")
			}
			return err
		}
		var current int
		if id == "" {
			if err := tx.QueryRow(ctx, "insert into services(name,owner,team_id) values($1,$2,$3) returning id", in.Name, u.ID, team).Scan(&id); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, "select current_version,routing_revision from services where id=$1 and team_id=$2 for update", id, team).Scan(&current, &routing); err != nil {
				return err
			}
		}
		if current != in.Expected || (in.ExpectedRoutingRevision != 0 && routing != in.ExpectedRoutingRevision) {
			return web.Fail(409, "服务版本已更新")
		}
		if in.ID != "" {
			if err := tx.QueryRow(ctx, "select max(version)+1 from service_versions where service_id=$1", id).Scan(&version); err != nil {
				return err
			}
			routing++
		}
		if _, err := tx.Exec(ctx, "insert into service_versions(service_id,version,name,instructions,reviewer,example_input) values($1,$2,$3,$4,$5,$6)", id, version, in.Name, in.Instructions, u.ID, in.Example); err != nil {
			return err
		}
		var count int64
		if in.Agent != "" {
			tag, err := tx.Exec(ctx, `insert into service_nodes select $1,$2,1,id,published_revision from agents where id=$3 and team_id=$4 and published_revision is not null and not disabled`, id, version, in.Agent, team)
			if err != nil {
				return err
			}
			count = tag.RowsAffected()
		} else {
			tag, err := tx.Exec(ctx, `insert into service_nodes select $1,$2,n.ordinal,n.agent_id,n.agent_revision,n.lookup_phase from projects p join plans v on (v.project_id,v.version)=(p.id,p.plan_version) join nodes n on(n.project_id,n.plan_version)=(p.id,p.plan_version) where p.id=$3 and p.owner=$4 and p.team_id=$5 and v.confirmed_by is not null`, id, version, in.Project, u.ID, team)
			if err != nil {
				return err
			}
			count = tag.RowsAffected()
		}
		if count == 0 {
			return web.Fail(409, "来源未发布、尚未确认或无权复用")
		}

		if in.Project != "" {
			if _, err := tx.Exec(ctx, `insert into service_tool_choices
 select $1,$2,c.ordinal,c.selector,c.alternative,c.alternative_revision
 from projects p join plan_tool_choices c on (c.project_id,c.plan_version)=(p.id,p.plan_version) where p.id=$3`, id, version, in.Project); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into service_edges select $1,$2,e.ordinal,e.depends_on from projects p join node_edges e on(e.project_id,e.plan_version)=(p.id,p.plan_version) where p.id=$3`, id, version, in.Project); err != nil {
				return err
			}
		}
		if err := checkServiceAdmission(ctx, tx, id, version, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update services set current_version=$1,name=$2,routing_revision=$3 where id=$4", version, in.Name, routing, id); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "service.publish", id, map[string]int{"version": version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, serviceRouting{ID: id, Version: version, RoutingRevision: routing})
}
func (s Server) issue(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Name string
		Days int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Name) || in.Days < 1 || in.Days > 365 {
		return web.Fail(422, "需要应用名称与 1–365 天有效期")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	token := web.Token()
	var principal string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var team string
		if err := tx.QueryRow(ctx, "select s.team_id from services s join team_members tm on tm.team_id=s.team_id and tm.member_id=$2 where s.id=$1 and s.enabled for share of s,tm", id, u.ID).Scan(&team); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "insert into members(email,display_name,password_hash,role,kind) values($1,$2,'!','member','application') returning id", "app-"+web.Token()+"@instance.invalid", in.Name).Scan(&principal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into team_members values($1,$2)", team, principal); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into applications(id,service_id,token_hash,expires_at) values($1,$2,$3,now()+make_interval(days=>$4))", principal, id, web.Hash(token), in.Days); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "application.issue", principal, map[string]string{"service": id})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": principal, "token": token})
}
func (s Server) delegate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		App   string
		Hours int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Hours < 1 || in.Hours > 48 {
		return web.Fail(422, "委托有效期为 1 至 48 小时")
	}
	id := r.PathValue("id")
	token := web.Token()
	ctx := r.Context()
	var did string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		// Serialize issuing a delegation with disabling its member. A request
		// authenticated just before suspension must not outlive that suspension.
		var enabled bool
		if err := tx.QueryRow(ctx, "select enabled from members where id=$1 for share", u.ID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return web.Fail(403, "成员登录已停用")
		}
		err := tx.QueryRow(ctx, `insert into delegations(app_id,member_id,token_hash,expires_at) select a.id,$3,$4,least(now()+make_interval(hours=>$5),a.expires_at) from applications a join services s on s.id=a.service_id join team_members t on t.team_id=s.team_id where a.id=$1 and a.service_id=$2 and t.member_id=$3 and a.revoked_at is null and a.expires_at>now() and s.enabled and exists(select from team_members principal join members pm on pm.id=principal.member_id and pm.enabled where principal.member_id=a.id and principal.team_id=s.team_id) returning id`, in.App, id, u.ID, web.Hash(token), in.Hours).Scan(&did)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "delegation.issue", did, map[string]string{"service": id, "app": in.App})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": did, "token": token})
}
func (s Server) revokeDelegation(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx, id := r.Context(), r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "update delegations set revoked_at=coalesce(revoked_at,now()) where id=$1 and member_id=$2", id, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return database.Audit(ctx, tx, u.ID, "delegation.revoke", id, map[string]bool{"revoked": true})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) revokeApp(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	id := r.PathValue("id")
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update applications a set revoked_at=coalesce(a.revoked_at,now()) from services s join team_members t on t.team_id=s.team_id and t.member_id=$2 where a.service_id=s.id and a.id=$1`, id, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if _, err = tx.Exec(ctx, "update members set enabled=false where id=$1 and kind='application'", id); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "application.revoke", id, map[string]bool{"revoked": true})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

type subject struct{ App, Actor, Service, Delegate string }

func authorize(ctx context.Context, tx pgx.Tx, r *http.Request) (subject, error) {
	var sub subject
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return sub, web.Fail(401, "需要有效应用凭证")
	}
	err := tx.QueryRow(ctx, `select a.id,a.service_id from applications a join services svc on svc.id=a.service_id join team_members tm on tm.team_id=svc.team_id and tm.member_id=a.id join members m on m.id=a.id where token_hash=$1 and revoked_at is null and expires_at>now() and m.enabled for share of a,m,tm`, web.Hash(token)).Scan(&sub.App, &sub.Service)
	if err == pgx.ErrNoRows {
		return sub, web.Fail(401, "应用凭证已失效")
	}
	if err != nil {
		return sub, err
	}
	sub.Actor = sub.App
	if d := r.Header.Get("X-Alliance-Delegation"); d != "" {
		err = tx.QueryRow(ctx, `select d.member_id,d.id from delegations d join members m on m.id=d.member_id join services s on s.id=$3 join team_members t on t.team_id=s.team_id and t.member_id=m.id where d.app_id=$1 and token_hash=$2 and revoked_at is null and expires_at>now() and m.enabled for share of d,m,t`, sub.App, web.Hash(d), sub.Service).Scan(&sub.Actor, &sub.Delegate)
		if err == pgx.ErrNoRows {
			return sub, web.Fail(403, "成员委托已失效")
		}
	}
	return sub, err
}

type request struct {
	AwaitLookupReceipt      bool
	AwaitCalculationReceipt bool
	Attachment              *Attachment
	Attachments             []Attachment
	Input                   string
	Version, AuthorizeCalls int
	documents               map[int]knowledge.ReadableDocument
	pictures                map[int]knowledge.Picture
}

func (r request) files() []Attachment {
	if r.Attachment != nil {
		return []Attachment{*r.Attachment}
	}
	return r.Attachments
}

func (r request) validateFiles() error {
	if r.Attachment != nil && r.Attachments != nil {
		return web.Fail(422, "请选择单份或多份资料提交格式")
	}
	files := r.files()
	if (r.Attachments != nil && len(files) == 0) || len(files) > maxAttachments {
		return web.Fail(422, "请提供 1 至 20 份资料")
	}
	total, textBytes, pictureBytes, pictures := 0, 0, 0, 0
	for _, file := range files {
		if knowledge.PictureMedia(file.Name) != "" {
			if err := validFileName(file.Name); err != nil {
				return err
			}
			if len(file.Data) == 0 || len(file.Data) > knowledge.PictureLimit {
				return web.Fail(422, "请选择非空且不超过 3 MiB 的图片")
			}
			pictures++
			pictureBytes += len(file.Data)
		} else if knowledge.DocumentFormat(file.Name) != "" {
			if err := validFileName(file.Name); err != nil {
				return err
			}
			if len(file.Data) == 0 {
				return web.Fail(422, "请提供非空文档")
			}
		} else {
			if err := file.Validate(); err != nil {
				return err
			}
			textBytes += len(file.Data)
		}
		total += len(file.Data)
	}
	if pictures > knowledge.MaxPictures || pictureBytes > knowledge.RunPictureLimit {
		return web.Fail(422, "服务最多接收 4 张图片，合计不超过 6 MiB")
	}
	if textBytes > AttachmentLimit {
		return web.Fail(422, "服务文本文件合计不能超过 96 KiB")
	}
	if total > knowledge.SourceFileLimit {
		return web.Fail(422, "服务资料原件合计不能超过 8 MiB")
	}
	return nil
}

func (r request) hasDocuments() bool {
	for _, file := range r.files() {
		if knowledge.DocumentFormat(file.Name) != "" {
			return true
		}
	}
	return false
}

func (r request) pictureCount() int {
	count := 0
	for _, file := range r.files() {
		if knowledge.PictureMedia(file.Name) != "" {
			count++
		}
	}
	return count
}

func (r *request) prepareOriginals(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r.documents = make(map[int]knowledge.ReadableDocument)
	r.pictures = make(map[int]knowledge.Picture)
	size := 0
	for i, file := range r.files() {
		if knowledge.PictureMedia(file.Name) != "" {
			picture, err := (knowledge.PictureUpload{Name: file.Name, Data: file.Data}).Validate()
			if err != nil {
				return err
			}
			r.pictures[i] = picture
		} else if knowledge.DocumentFormat(file.Name) == "" {
			size += knowledge.InputSourceSize(file.Name, 1, len(file.Data))
		} else {
			doc, err := knowledge.PrepareReadableDocument(ctx, file.Name, file.Data)
			if err != nil {
				return err
			}
			r.documents[i] = doc
			size += doc.ContextSize()
		}
		if size > knowledge.RunContextLimit {
			return web.Fail(422, "资料可读取内容合计超过本次任务的 96 KiB 容量，请减少文件或选取相关部分")
		}
	}
	return nil
}

func checkServiceAdmission(ctx context.Context, tx pgx.Tx, id string, version int, actor string) error {
	var blocked bool
	err := tx.QueryRow(ctx, `select exists(select from (`+serviceDependencies+`) refs
 where version=$2 and not agent_admissible(agent_id,agent_revision,$3))`, id, version, actor).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return web.Fail(409, "服务依赖的能力待复验、停用或未获授权，请联系维护者")
	}
	return nil
}

type serviceAdmission struct {
	team, reviewer, instructions, name string
	count                              int
}

func admitService(ctx context.Context, tx pgx.Tx, sub subject, input request) (info serviceAdmission, err error) {
	var currentVersion int
	var calculation, lookup, csvChoice, needsSource, acceptsDocuments, usesPictures, editsPicture bool
	err = tx.QueryRow(ctx, `select s.team_id,v.reviewer,v.instructions,coalesce(v.name,s.name),s.current_version,
 (select count(*) from service_nodes where service_id=s.id and version=v.version),
 exists(select from service_nodes where service_id=s.id and version=v.version and lookup_phase='answer_calc'),
 exists(select from service_nodes where service_id=s.id and version=v.version and lookup_phase='answer'),
 service_requires_csv_choice(s.id,v.version),
 exists(select from service_nodes where service_id=s.id and version=v.version and lookup_phase='search'), `+serviceAcceptsDocuments+`, `+serviceUsesPictures+`, `+serviceEditsPicture+`
 from services s join service_versions v on v.service_id=s.id join team_members t on t.team_id=s.team_id
 join members m on m.id=v.reviewer and m.enabled where s.id=$1 and v.version=$2 and s.enabled and t.member_id=$3
 and exists(select from team_members where team_id=s.team_id and member_id=v.reviewer) for share of s,t,m`,
		sub.Service, input.Version, sub.Actor).Scan(&info.team, &info.reviewer, &info.instructions, &info.name, &currentVersion, &info.count, &calculation, &lookup, &csvChoice, &needsSource, &acceptsDocuments, &usesPictures, &editsPicture)
	if err != nil {
		return info, err
	}
	if sub.App == "" && currentVersion != input.Version {
		return info, web.Fail(409, "服务已换版，请核对新版本后重新确认")
	}
	if err = checkServiceAdmission(ctx, tx, sub.Service, input.Version, sub.Actor); err != nil {
		return info, err
	}
	if input.AwaitCalculationReceipt && !calculation {
		return info, web.Fail(422, "该服务版本没有可等待补件的金额核算步骤")
	}
	if input.AwaitLookupReceipt && !lookup {
		return info, web.Fail(422, "该服务版本没有可等待补件的原文问答步骤")
	}
	pictures := input.pictureCount()
	if editsPicture && pictures != 1 {
		return info, web.Fail(422, "参考图编辑服务需要恰好一张 PNG 或 JPEG 原件")
	}
	if usesPictures && pictures == 0 {
		return info, web.Fail(422, "此服务需要附上 PNG 或 JPEG 图片")
	}
	if !usesPictures && pictures > 0 {
		return info, web.Fail(422, "此服务没有读取图片的步骤，请移除图片或选用支持图片的服务")
	}
	files := input.files()
	if input.hasDocuments() && !acceptsDocuments {
		return info, web.Fail(422, "此服务的汇总步骤需要 CSV 资料，请改用 CSV 后再启动")
	}
	if csvChoice {
		if len(files) == 0 {
			return info, web.Fail(422, "此服务需要 CSV 资料以选择并执行汇总")
		}
		for _, file := range files {
			if !strings.EqualFold(filepath.Ext(file.Name), ".csv") {
				return info, web.Fail(422, "此服务需要 CSV 资料以选择并执行汇总")
			}
		}
	}
	if input.AuthorizeCalls != info.count {
		return info, web.Fail(422, "本次调用范围与服务步骤数不符")
	}
	if needsSource {
		textSource := false
		for _, file := range files {
			textSource = textSource || knowledge.PictureMedia(file.Name) == ""
		}
		if !textSource {
			return info, web.Fail(422, "此服务需要一份可读资料以检索原文")
		}
	}
	return info, nil
}

// CheckMemberAttachment verifies scope and the named input before fetching an original.
// LaunchMember repeats admission when it starts the run with the actual bytes.
func CheckMemberAttachment(ctx context.Context, tx pgx.Tx, actor, serviceID, name string, version, calls int) error {
	if err := ValidateAttachmentName(name); err != nil {
		return err
	}
	_, err := admitService(ctx, tx, subject{Actor: actor, Service: serviceID},
		request{Version: version, AuthorizeCalls: calls, Attachment: &Attachment{Name: name}})
	return err
}

func (input request) fingerprint(sub subject) [32]byte {
	sig := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%d/%d/%s", sub.App, sub.Service, sub.Delegate, input.Version, input.AuthorizeCalls, input.Input)))
	// Keep the historical fingerprint for a single file. Each subsequent
	// original extends the same framed digest in the supplied order.
	for _, file := range input.files() {
		body, _ := json.Marshal(struct {
			Original [32]byte
			Name     string
			File     [32]byte
		}{sig, file.Name, sha256.Sum256(file.Data)})
		sig = sha256.Sum256(body)
	}
	if input.AwaitCalculationReceipt {
		sig = sha256.Sum256(append([]byte("await-calculation-receipt/"), sig[:]...))
	}
	if input.AwaitLookupReceipt {
		sig = sha256.Sum256(append([]byte("await-lookup-receipt/"), sig[:]...))
	}
	return sig
}

func serviceReplay(ctx context.Context, tx pgx.Tx, sub subject, key string, sig [32]byte) (map[string]string, error) {
	var run, project string
	var matches bool
	err := tx.QueryRow(ctx, "select id,project_id,request_hash=$3 from runs where actor=$1 and request_key=$2", sub.Actor, key, sig[:]).Scan(&run, &project, &matches)
	if err == nil {
		if !matches {
			return nil, web.Fail(409, "幂等键已用于不同服务输入")
		}
		var canRead bool
		if err := tx.QueryRow(ctx, "select exists(select from project_members pm join services s on s.id=$3 join team_members t on t.team_id=s.team_id and t.member_id=pm.member_id where pm.project_id=$1 and pm.member_id=$2)", project, sub.Actor, sub.Service).Scan(&canRead); err != nil {
			return nil, err
		}
		if !canRead {
			return nil, pgx.ErrNoRows
		}
		return map[string]string{"id": run, "project": project}, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	return nil, nil
}

func launch(ctx context.Context, tx pgx.Tx, sub subject, input request, key string) (map[string]string, error) {
	if err := input.validateFiles(); err != nil {
		return nil, err
	}
	sig := input.fingerprint(sub)
	// Serialize repeated submissions before allocating projects and plans.
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", sub.Actor+"/"+key); err != nil {
		return nil, err
	}
	if result, err := serviceReplay(ctx, tx, sub, key, sig); result != nil || err != nil {
		return result, err
	}
	var run, project string
	admission, err := admitService(ctx, tx, sub, input)
	if err != nil {
		return nil, err
	}
	team, reviewer, instructions, name, count := admission.team, admission.reviewer, admission.instructions, admission.name, admission.count
	if err = tx.QueryRow(ctx, "insert into projects(owner,team_id,final_reviewer,title,stage,plan_version) values($1,$2,$3,$4,'running',1) returning id", sub.Actor, team, reviewer, name).Scan(&project); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "insert into project_memberships values($1,$2,false),($1,$3,true) on conflict do nothing", project, sub.Actor, reviewer); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "update project_members set can_review=true where project_id=$1 and member_id=$2", project, reviewer); err != nil {
		return nil, err
	}
	if err = projects.Note(ctx, tx, project, "user", input.Input, sub.Actor); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "insert into plans values($1,1,$2,$3)", project, instructions+"\n本次输入：\n"+input.Input, sub.Actor); err != nil {
		return nil, err
	}
	for i, file := range input.files() {
		if err := file.pin(ctx, tx, project, sub.Actor, input.documents[i], input.pictures[i], i+1); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, "insert into nodes select $1,1,ordinal,agent_id,agent_revision,lookup_phase from service_nodes where service_id=$2 and version=$3", project, sub.Service, input.Version); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into plan_tool_choices select $1,1,ordinal,selector,alternative,alternative_revision
 from service_tool_choices where service_id=$2 and version=$3`, project, sub.Service, input.Version); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into plan_rules select $1,1,id,published_revision from team_rules where team_id=$2 and not disabled and published_revision is not null`, project, team); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "insert into node_edges select $1,1,ordinal,depends_on from service_edges where service_id=$2 and version=$3", project, sub.Service, input.Version); err != nil {
		return nil, err
	}
	if err = projects.CheckAdmission(ctx, tx, project, 1, sub.Actor); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,app_id,delegated_by,service_id,service_version,await_calculation_receipt,await_lookup_receipt) values($1,1,$2,$3,$4,$5,$6,nullif($7,'')::uuid,nullif($8,'')::uuid,$9,$10,$11,$12) returning id`, project, sub.Actor, team, key, sig[:], count, sub.App, sub.Delegate, sub.Service, input.Version, input.AwaitCalculationReceipt, input.AwaitLookupReceipt).Scan(&run)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "insert into outbox(run_id) values($1)", run); err != nil {
		return nil, err
	}
	if err = database.Audit(ctx, tx, sub.Actor, "service.invoke", run, map[string]any{"service": sub.Service, "app": sub.App, "version": input.Version, "awaitCalculationReceipt": input.AwaitCalculationReceipt, "awaitLookupReceipt": input.AwaitLookupReceipt}); err != nil {
		return nil, err
	}
	return map[string]string{"id": run, "project": project}, nil
}

// LaunchMember uses the same admission, billing and idempotency rules as the web service entry.
type SourceWait struct {
	Lookup, Calculation bool
}

func LaunchMember(ctx context.Context, tx pgx.Tx, actor, serviceID, input, key string, version, calls int, file *Attachment, wait SourceWait) (map[string]string, error) {
	if file != nil {
		if err := file.Validate(); err != nil {
			return nil, err
		}
	}
	return launch(ctx, tx, subject{Actor: actor, Service: serviceID}, request{
		Input: input, Version: version, AuthorizeCalls: calls, Attachment: file,
		AwaitLookupReceipt: wait.Lookup, AwaitCalculationReceipt: wait.Calculation,
	}, key)
}

func (s Server) chat(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.invoke(w, r, u.ID)
}
func (s Server) start(w http.ResponseWriter, r *http.Request) error { return s.invoke(w, r, "") }

func invocationSubject(r *http.Request, tx pgx.Tx, actor string) (subject, error) {
	ctx := r.Context()
	id := r.PathValue("id")
	if actor == "" {
		sub, err := authorize(ctx, tx, r)
		if err == nil && sub.Service != id {
			err = pgx.ErrNoRows
		}
		return sub, err
	}
	var idCheck string
	err := tx.QueryRow(ctx, "select id from members where id=$1 and enabled and kind='person' for share", actor).Scan(&idCheck)
	return subject{Actor: actor, Service: id}, err
}

func (s Server) invoke(w http.ResponseWriter, r *http.Request, actor string) error {
	ctx := r.Context()
	if actor == "" {
		// Authenticate the signed caller before reading a document-sized body.
		if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			_, err := invocationSubject(r, tx, actor)
			return err
		}); err != nil {
			return err
		}
	}
	var in request
	if err := web.ReadLimited(w, r, &in, serviceRequestLimit); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 160 || web.Blank(in.Input) || len(in.Input) > 64000 {
		return web.Fail(422, "需要幂等键和有界的业务输入")
	}
	if err := in.validateFiles(); err != nil {
		return err
	}
	var result map[string]string
	if in.hasDocuments() || in.pictureCount() > 0 {
		// Decode new originals only after admission and before the transaction that pins them.
		if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			sub, err := invocationSubject(r, tx, actor)
			if err != nil {
				return err
			}
			result, err = serviceReplay(ctx, tx, sub, key, in.fingerprint(sub))
			if result != nil || err != nil {
				return err
			}
			_, err = admitService(ctx, tx, sub, in)
			return err
		}); err != nil {
			return err
		}
		if result == nil {
			if err := in.prepareOriginals(ctx); err != nil {
				return err
			}
		}
	}
	if result == nil {
		if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			sub, err := invocationSubject(r, tx, actor)
			if err != nil {
				return err
			}
			result, err = launch(ctx, tx, sub, in, key)
			return err
		}); err != nil {
			return err
		}
	}
	result["statusUrl"] = "/api/v1/runs/" + result["id"]
	if actor != "" {
		result["statusUrl"] = "/api/projects/" + result["project"]
	}
	return web.JSON(w, 202, result)
}
func (s Server) progress(w http.ResponseWriter, r *http.Request) error {
	return s.read(w, r, "progress")
}
func (s Server) delivery(w http.ResponseWriter, r *http.Request) error {
	return s.read(w, r, "manifest")
}
func (s Server) deliveryPackage(w http.ResponseWriter, r *http.Request) error {
	return s.read(w, r, "package")
}
func (s Server) read(w http.ResponseWriter, r *http.Request, view string) error {
	ctx := r.Context()
	var body json.RawMessage
	var delivery string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		v, err := authorizeRun(ctx, tx, r)
		if err != nil {
			return err
		}

		if view != "progress" {
			delivery, err = knowledge.SelectDelivery(r, tx, v.Project)
			if err != nil {
				return err
			}
			if view == "package" {
				return nil
			}
			var state string
			err = tx.QueryRow(ctx, `select distribution_state(d.id),case when distribution_state(d.id) in('clear','reference') then jsonb_build_object('delivery',d.id,'edition',c.edition,'previous',c.previous,'changes',to_jsonb(c.changes),'changedNotes',to_jsonb(c.changed_notes),'handoff',d.handoff,'items',d.items,'isPartial',d.partial,'coverage',d.coverage) end from delivery_recipient_manifests d join delivery_comparisons c on c.id=d.id where d.id=$1`, delivery).Scan(&state, &body)
			if err != nil {
				return err
			}
			w.Header().Set("X-Delivery-Distribution", state)
			return knowledge.DistributionError(state)
		}
		return tx.QueryRow(ctx, progressSQL, r.PathValue("id"), v.Actor).Scan(&body)
	})
	if err != nil {
		return err
	}
	if view == "package" {
		return knowledge.WriteDeliveryPackage(w, r, s.DB, knowledge.ConfiguredObjects(), delivery, func() error {
			return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error { _, err := authorizeRun(ctx, tx, r); return err })
		})
	}
	return web.JSON(w, 200, body)
}
