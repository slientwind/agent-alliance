package services

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) entry(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	result, err := readEntry(r.Context(), s.DB, r.PathValue("id"), u.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func readEntry(ctx context.Context, q resources.Query, id, actor string) (json.RawMessage, error) {
	var result json.RawMessage
	err := q.QueryRow(ctx, `select jsonb_build_object('id',s.id,'name',s.name,'version',v.version,'brief',v.instructions,'example',v.example_input,
 'team',t.name,'reviewer',reviewer.display_name,'deliveryContract',service_delivery_contract(s.id,v.version),
 'allowDocumentAttachments',`+serviceAcceptsDocuments+`,'requiresPictures',`+serviceUsesPictures+`,'editsImage',`+serviceEditsPicture+`,
 'ready',reviewer.enabled and exists(select from team_members tm where tm.team_id=s.team_id and tm.member_id=v.reviewer) and not exists(select from service_nodes n where(n.service_id,n.version)=(s.id,v.version) and not agent_admissible(n.agent_id,n.agent_revision,$2))
 and not exists(select from service_tool_choices c where (c.service_id,c.version)=(s.id,v.version) and not agent_admissible(c.alternative,c.alternative_revision,$2)),
 'steps',array(select jsonb_build_object('name',a.name,'revision',n.agent_revision,'lookupPhase',n.lookup_phase,'operation',agent_output_operation(a),'toolChoice',
 (select jsonb_build_object('selector',c.selector,'name',other.name,'revision',other.revision,'primaryConfig',aggregate_scope(a.tool),'config',aggregate_scope(other.tool),
  'primaryRemote',a.card is not null,'alternativeRemote',other.card is not null) from service_tool_choices c
 join agent_revisions other on(other.agent_id,other.revision)=(c.alternative,c.alternative_revision)
 where(c.service_id,c.version,c.ordinal)=(n.service_id,n.version,n.ordinal))) from service_nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision) where(n.service_id,n.version)=(s.id,v.version) order by n.ordinal),
 'recent',array(select jsonb_build_object('id',p.id,'version',r.service_version,'stage',p.stage,'input',left(msg.body,75)) from runs r join projects p on p.id=r.project_id join project_members pm on pm.project_id=p.id and pm.member_id=$2 left join lateral(select body from messages where project_id=p.id and kind='user' order by seq limit 1)msg on true
 where r.service_id=s.id and r.actor=$2 order by p.created_at desc,p.id limit 20))
 from services s join service_versions v on(v.service_id,v.version)=(s.id,s.current_version) join teams t on t.id=s.team_id join team_members member on member.team_id=s.team_id and member.member_id=$2 join members reviewer on reviewer.id=v.reviewer where s.id=$1 and s.enabled`, id, actor).Scan(&result)
	return result, err
}

// Attachment is the original file accepted by a service entry point.
type Attachment struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

const AttachmentLimit = 96 << 10
const maxAttachments = 20
const serviceRequestLimit = 12 << 20

const serviceAcceptsDocuments = `not exists(select from service_nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 where (n.service_id,n.version)=(s.id,v.version) and a.tool->>'operation'='aggregate')`

const serviceUsesPictures = `exists(select from service_nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 where (n.service_id,n.version)=(s.id,v.version) and agent_uses_picture(a))`

const serviceEditsPicture = `exists(select from service_nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 where (n.service_id,n.version)=(s.id,v.version) and agent_edits_picture(a))`

func ValidateAttachmentName(name string) error {
	if err := validFileName(name); err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".json":
	default:
		return web.Fail(422, "此入口可读取 TXT、Markdown、CSV 和 JSON 资料")
	}
	return nil
}

func validFileName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 250 || strings.ContainsAny(name, "/\\\r\n\x00") {
		return web.Fail(422, "资料名称无效")
	}
	return nil
}

func (f Attachment) Validate() error {
	if len(f.Data) == 0 || len(f.Data) > AttachmentLimit {
		return web.Fail(422, "请提供非空且不超过 96 KiB 的文本资料")
	}
	if err := ValidateAttachmentName(f.Name); err != nil {
		return err
	}
	if !utf8.Valid(f.Data) || bytes.IndexByte(f.Data, 0) >= 0 {
		return web.Fail(422, "资料须使用 UTF-8 文本编码")
	}
	return nil
}

func (f Attachment) pin(ctx context.Context, tx pgx.Tx, project, actor string, document knowledge.ReadableDocument, picture knowledge.Picture, position int) error {
	objects := knowledge.ConfiguredObjects()
	var id string
	var err error
	if knowledge.DocumentFormat(f.Name) != "" {
		id, err = document.Save(ctx, tx, objects, project, actor)
	} else if knowledge.PictureMedia(f.Name) != "" {
		if picture.Media == "" {
			return web.Fail(422, "请先完成图片原件检查")
		}
		id, err = objects.NewPictureSource(ctx, tx, project, actor, picture)
	} else {
		id, err = objects.NewTextSource(ctx, tx, project, actor, f.Name, string(f.Data))
	}
	if err != nil {
		return err
	}
	var order *int
	if picture.Media != "" {
		order = &position
	}
	if _, err = tx.Exec(ctx, "insert into plan_sources(project_id,plan_version,source_id,source_version,picture_order) values($1,1,$2,1,$3)", project, id, order); err != nil {
		return err
	}
	return nil
}
