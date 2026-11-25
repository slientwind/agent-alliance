package knowledge

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/memory"
	"alliance/internal/web"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Server struct {
	DB      *pgxpool.Pool
	Objects Objects
}

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	s.rulesRoutes(m, g)
	m.Handle("POST /api/projects/{id}/sources/{source}/extract", g.Protect(false, s.retryExtraction))
	m.Handle("GET /api/projects/{id}/retrieval", g.Protect(false, s.retrieve))
	m.Handle("POST /api/projects/{id}/sources/{source}/lifecycle", g.Protect(false, s.sourceLifecycle))
	for path, h := range map[string]identity.Action{
		"GET /api/projects/{id}/delivery/package":          s.deliveryPackage,
		"GET /api/agents/{id}/evidence/{trial}/file":       s.trialFile,
		"GET /api/agents/{id}/evidence/{trial}/picture":    s.trialPicture,
		"GET /api/projects/{id}/change-runs/{change}/file": s.changeFile,
		"GET /api/projects/{id}/artifacts/{artifact}/file": s.artifact,
		"GET /api/projects/{id}/sources":                   s.list, "POST /api/projects/{id}/sources": s.upload, "PUT /api/projects/{id}/sources/{source}": s.upload,
		"GET /api/projects/{id}/sources/{source}/compare": s.compareSource,
		"GET /api/projects/{id}/sources/{source}/history": s.sourceHistory,
		"GET /api/projects/{id}/sources/{source}/content": s.content, "DELETE /api/projects/{id}/sources/{source}": s.revoke,
		"POST /api/preferences/{id}/actions": s.preferenceAction, "GET /api/preferences": s.preferences, "POST /api/preferences": s.savePreference, "PATCH /api/preferences/{id}": s.savePreference, "DELETE /api/preferences/{id}": s.deletePreference,
	} {
		m.Handle(path, g.Protect(false, h))
	}
}
func (s Server) list(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select s.id,s.name,s.current_version,s.revoked_at is not null, v.media_type,(s.revoked_at is null and v.extracted is not null),s.revoked_at is null and source_is_picture(v.media_type),coalesce(o.size_bytes,0),(s.created_by=$1 or p.owner=$1),s.recycled_at,s.purge_due,s.purged_at,exists(select from source_object_cleanup c where c.source_id=s.id),s.recovered_as,exists(select from source_object_cleanup c where c.source_id=s.id and c.last_failure is not null),s.shared_files_retained,v.extraction_state from sources s join projects p on p.id=s.project_id join source_versions v on(v.source_id,v.version)=(s.id,s.current_version) left join objects o on o.hash=v.object_hash join project_members m on m.project_id=s.project_id where m.member_id=$1 and s.project_id=$2 order by s.name,s.id`, u.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	type info struct {
		ID             string     `json:"id"`
		Name           string     `json:"name"`
		Version        int        `json:"version"`
		Revoked        bool       `json:"revoked"`
		Media          string     `json:"media"`
		Readable       bool       `json:"readable"`
		Picture        bool       `json:"picture,omitempty"`
		Size           int64      `json:"size"`
		CanMaintain    bool       `json:"canMaintain"`
		RecycledAt     *time.Time `json:"recycledAt,omitempty"`
		PurgeDue       *time.Time `json:"purgeDue,omitempty"`
		PurgedAt       *time.Time `json:"purgedAt,omitempty"`
		CleanupPending bool       `json:"cleanupPending,omitempty"`
		RecoveredAs    *string    `json:"recoveredAs,omitempty"`
		CleanupFailed  bool       `json:"cleanupFailed,omitempty"`
		SharedFiles    int        `json:"sharedFiles,omitempty"`
		Extraction     string     `json:"extraction,omitempty"`
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[info])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, list)
}

const SourceFileLimit = 8 << 20
const AttachmentLimit = 25 << 20
const RunContextLimit = 96 << 10

func (s Server) upload(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	project := r.PathValue("id")
	id := r.PathValue("source")
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len(name) > 250 {
		return web.Fail(422, "需要文件名称")
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 || (key != "" && id != "") {
		return web.Fail(422, "上传标识只用于新增资料，最长 128 字节")
	}
	expected, _ := strconv.Atoi(r.URL.Query().Get("version"))
	var allowed bool
	err := s.DB.QueryRow(ctx, "select exists(select from project_members where project_id=$1 and member_id=$2)", project, u.ID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return web.Fail(404, "项目不可见")
	}
	if id != "" {
		var author, owner string
		if err := s.DB.QueryRow(ctx, "select s.created_by,p.owner from sources s join projects p on p.id=s.project_id where s.id=$1 and p.id=$2 and s.revoked_at is null", id, project).Scan(&author, &owner); err != nil {
			return err
		}
		if u.ID != author && u.ID != owner {
			return web.Fail(403, "需要资料维护权限")
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, AttachmentLimit+1))
	if err != nil {
		return err
	}
	if len(raw) > AttachmentLimit {
		return web.Fail(413, "附件最多 25 MiB")
	}
	media := "application/octet-stream"
	var extracted extraction
	isPicture := PictureMedia(name) != ""
	if isPicture {
		picture, err := (PictureUpload{Name: name, Data: raw}).Validate()
		if err != nil {
			return err
		}
		media = picture.Media
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".json":
		if len(raw) <= 256<<10 && utf8.Valid(raw) && bytes.IndexByte(raw, 0) < 0 {
			text := string(raw)
			extracted.text = &text
			media = "text/plain; charset=utf-8"
		}
	}
	if format := DocumentFormat(name); format != "" {
		extracted = extractDocument(ctx, format, raw)
	}
	ver := 1
	readable := extracted.text != nil
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var owner string
		var revision int
		if err := tx.QueryRow(ctx, `select p.owner,p.revision from projects p join project_members m on m.project_id=p.id where m.member_id=$1 and p.id=$2 for update of p`, u.ID, project).Scan(&owner, &revision); err != nil {
			return err
		}
		if q := r.URL.Query(); id != "" && q.Has("expectedProject") && q.Get("expectedProject") != strconv.Itoa(revision) {
			return web.Fail(409, "项目引用已变化，请重新查看影响")
		}
		if id == "" {
			if key != "" {
				var same bool
				err := tx.QueryRow(ctx, `select s.id, coalesce(s.revoked_at is null and s.current_version=1 and v.filename=$4 and v.object_hash=$5,false),v.extracted is not null,v.extraction_state,source_is_picture(v.media_type)
                 from sources s join source_versions v on v.source_id=s.id and v.version=1
                 where s.project_id=$1 and s.created_by=$2 and s.upload_key=$3`, project, u.ID, key, name, fmt.Sprintf("%x", sha256.Sum256(raw))).Scan(&id, &same, &readable, &extracted.state, &isPicture)
				if err == nil {
					if !same {
						return web.Fail(409, "上传标识已使用或资料已变化，请核对现有版本")
					}
					return nil
				}
				if err != pgx.ErrNoRows {
					return err
				}
			}
			if err := tx.QueryRow(ctx, "insert into sources(project_id,name,created_by,upload_key) values($1,$2,$3,nullif($4,'')) returning id", project, name, u.ID, key).Scan(&id); err != nil {
				return err
			}
		} else {
			var author string
			if err := tx.QueryRow(ctx, "select current_version,created_by from sources where id=$1 and project_id=$2 and revoked_at is null for update", id, project).Scan(&ver, &author); err != nil {
				return err
			}
			if owner != u.ID && author != u.ID {
				return web.Fail(403, "需要资料维护权限")
			}
			if expected != ver {
				return web.Fail(409, "资料版本已更新")
			}
			ver++
			if _, err := tx.Exec(ctx, "update sources set current_version=$1,name=$2 where id=$3", ver, name, id); err != nil {
				return err
			}
		}
		if err := s.Objects.saveSourceVersion(ctx, tx, id, u.ID, name, ver, bytes.NewReader(raw), AttachmentLimit, media, extracted); err != nil {
			return err
		}
		if ver > 1 {
			if err := recordSourceChange(ctx, tx, project, id, "source_updated"); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "source.save", id, map[string]int{"version": ver})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "version": ver, "readable": readable, "extraction": extracted.state, "picture": isPicture})
}

// NewTextSource registers a readable first revision in the caller's project transaction.
func (o Objects) NewTextSource(ctx context.Context, tx pgx.Tx, project, actor, name, text string) (string, error) {
	return o.newReadableSource(ctx, tx, project, actor, name, strings.NewReader(text), 256<<10, "text/plain; charset=utf-8", extraction{text: &text})
}

func (o Objects) newReadableSource(ctx context.Context, tx pgx.Tx, project, actor, name string, input io.Reader, limit int64, media string, data extraction) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, "insert into sources(project_id,name,created_by) values($1,$2,$3) returning id", project, name, actor).Scan(&id); err != nil {
		return "", err
	}
	if err := o.saveSourceVersion(ctx, tx, id, actor, name, 1, input, limit, media, data); err != nil {
		return "", err
	}
	return id, database.Audit(ctx, tx, actor, "source.save", id, map[string]int{"version": 1})
}

func (o Objects) saveSourceVersion(ctx context.Context, tx pgx.Tx, id, actor, name string, version int, input io.Reader, limit int64, media string, data extraction) error {
	hash, _, err := o.Put(ctx, tx, input, limit)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state,extraction_locations) values($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, version, hash, media, data.text, actor, name, data.state, data.locations)
	return err
}
func (s Server) content(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ver, parseErr := strconv.Atoi(r.URL.Query().Get("version"))
	if r.URL.Query().Has("version") && (parseErr != nil || ver <= 0) {
		return web.Fail(422, "资料版本无效")
	}
	return s.authorizedFile(w, r, func() (fileRef, error) {
		var ref fileRef
		err := s.DB.QueryRow(r.Context(), `select v.object_hash,coalesce(v.filename,'historical-source'),v.media_type from sources s join project_members m on m.project_id=s.project_id join source_versions v on v.source_id=s.id and v.version=case when $4=0 then s.current_version else $4 end where m.member_id=$1 and s.project_id=$2 and s.id=$3 and s.revoked_at is null`, u.ID, r.PathValue("id"), r.PathValue("source"), ver).Scan(&ref.hash, &ref.name, &ref.media)
		return ref, err
	})
}
func (s Server) revoke(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	id := r.PathValue("source")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var revision int
		if err := tx.QueryRow(ctx, "select p.revision from projects p join project_members pm on pm.project_id=p.id where p.id=$1 and pm.member_id=$2 for update of p", r.PathValue("id"), u.ID).Scan(&revision); err != nil {
			return err
		}
		if q := r.URL.Query(); q.Has("expectedProject") && q.Get("expectedProject") != strconv.Itoa(revision) {
			return web.Fail(409, "项目引用已变化，请重新查看影响")
		}
		tag, err := tx.Exec(ctx, `update sources s set revoked_at=now() from projects p join project_members pm on pm.project_id=p.id where s.id=$1 and s.project_id=p.id and p.id=$2 and pm.member_id=$3 and (s.created_by=$3 or p.owner=$3) and s.revoked_at is null and ($4='' or s.current_version::text=$4)`, id, r.PathValue("id"), u.ID, r.URL.Query().Get("version"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(404, "资料不可用")
		}
		if err := recordSourceChange(ctx, tx, r.PathValue("id"), id, "source_revoked"); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "source.revoke", id, map[string]bool{"revoked": true})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func recordSourceChange(ctx context.Context, tx pgx.Tx, project, source, state string) error {
	if _, err := tx.Exec(ctx, `update projects p set revision=revision+1,updated_at=now() where id=$1 and (
      exists(select from plan_sources ps where(ps.project_id,ps.plan_version,ps.source_id)=(p.id,p.plan_version,$2))
      or exists(select from run_source_receipt_pins p join runs r on r.id=p.run_id where r.project_id=$1 and p.source_id=$2))`, project, source); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `select record_service_event(r.id,'project',$3) from runs r where r.project_id=$1 and (
      exists(select from plan_sources ps where(ps.project_id,ps.plan_version,ps.source_id)=(r.project_id,r.plan_version,$2))
      or exists(select from run_source_receipt_pins p where p.run_id=r.id and p.source_id=$2)) order by r.id`, project, source, state)
	return err
}

type InputSource struct {
	ReceiptStep, ExcerptStart, ExcerptEnd int               `json:"-"`
	Locations                             []byte            `json:"-"`
	ColumnMapping                         map[string]string `json:"columnMapping,omitempty"`
	ID                                    string            `json:"id"`
	Name                                  string            `json:"name"`
	Version                               int               `json:"version"`
	Text                                  string            `json:"text"`
}

func Inputs(ctx context.Context, tx pgx.Tx, run string) (string, []InputSource, error) {
	return RunInputs(ctx, tx, run, 0, true)
}

func inputSourceHeader(name string, version int) string {
	return fmt.Sprintf("\n资料 %s v%d\n", name, version)
}

func InputSourceSize(name string, version, textBytes int) int {
	return len(inputSourceHeader(name, version)) + textBytes + 1
}

// RunInputs keeps the same pins, limits and authorization checks when the model
// only needs the source names. The local search tool receives the full files.
func RunInputs(ctx context.Context, tx pgx.Tx, run string, step int, includeText bool) (string, []InputSource, error) {
	return runInputs(ctx, tx, run, step, includeText, nil)
}

func CapturedRunInputs(ctx context.Context, tx pgx.Tx, run string, step int, includeText bool) (string, []InputSource, []memory.Reference, error) {
	var refs []memory.Reference
	text, files, err := runInputs(ctx, tx, run, step, includeText, &refs)
	return text, files, refs, err
}

func runInputs(ctx context.Context, tx pgx.Tx, run string, step int, includeText bool, refs *[]memory.Reference) (string, []InputSource, error) {
	inputBytes := 0
	rows, err := tx.Query(ctx, `select source_id,filename,version,body,locations,receipt_step,excerpt_start,excerpt_end,picture,available
      from (
        select f.source_id,f.filename,f.version,
        case when f.extracted is null or (pin.receipt_step is not null and f.version<>f.plan_pin) then null
             when $3<>0 and pin.receipt_step<>$3 then '' else f.extracted end body,
        case when not $2 and ($3=0 or pin.receipt_step is null or pin.receipt_step=$3) then v.extraction_locations end locations,
        coalesce(pin.receipt_step,0) receipt_step,coalesce(pin.excerpt_start,0) excerpt_start,coalesce(pin.excerpt_end,0) excerpt_end,
        f.picture_media is not null picture,f.picture_hash is not null available,0 as scoped,coalesce(pin.receipt_order,0) as ordering
        from run_source_inputs f join source_versions v on (v.source_id,v.version)=(f.source_id,f.version)
        join runs r on r.id=f.run_id left join plan_sources pin on (pin.project_id,pin.plan_version,pin.source_id)=(r.project_id,r.plan_version,f.source_id)
        where f.run_id=$1
        union all
        select p.source_id,coalesce(v.filename,'历史资料'),p.source_version,
            case when s.revoked_at is null then v.extracted end,
            case when not $2 then v.extraction_locations end,
            p.reader,p.excerpt_start,p.excerpt_end,false,false,1,p.request_revision
        from step_source_receipt_inputs p join runs r on r.id=p.run_id
        join sources s on (s.id,s.project_id)=(p.source_id,r.project_id)
        join source_versions v on (v.source_id,v.version)=(p.source_id,p.source_version)
        where (p.run_id,p.reader)=($1,$3)
      ) evidence order by scoped,ordering,source_id`, run, includeText, step)
	if err != nil {
		return "", nil, err
	}
	var files []InputSource
	var b strings.Builder
	for rows.Next() {
		var id, name string
		var ver int
		var body *string
		var locations []byte
		var receipt, from, end int
		var picture, available bool
		if err = rows.Scan(&id, &name, &ver, &body, &locations, &receipt, &from, &end, &picture, &available); err != nil {
			rows.Close()
			return "", nil, err
		}
		if picture && available && receipt == 0 {
			if refs != nil {
				*refs = append(*refs, memory.Reference{Kind: "source", ID: id, Revision: ver})
			}
			continue
		}
		if body == nil {
			rows.Close()
			return "", nil, web.Fail(409, "资料已撤权、指定新版发生变化或不可读取")
		}
		// Downstream outputs may quote a receipt. Check every pin before
		// dispatch, then pass only this step's authorized sources to its Agent.
		if receipt != 0 && step != 0 && receipt != step {
			continue
		}
		input := *body
		if receipt != 0 {
			text := []rune(input)
			if from < 0 || end > len(text) || from >= end {
				rows.Close()
				return "", nil, web.Fail(409, "补件原句的位置已失效")
			}
			input = string(text[from:end])
		}
		inputBytes += InputSourceSize(name, ver, len(input))
		if inputBytes > RunContextLimit {
			rows.Close()
			return "", nil, web.Fail(409, "资料已撤权、指定新版发生变化、不可读取或超过上下文容量")
		}
		files = append(files, InputSource{ID: id, Name: name, Version: ver, Text: *body, Locations: locations, ReceiptStep: receipt, ExcerptStart: from, ExcerptEnd: end})
		if refs != nil {
			*refs = append(*refs, memory.Reference{Kind: "source", ID: id, Revision: ver})
		}
		b.WriteString(inputSourceHeader(name, ver))
		if includeText {
			b.WriteString(input)
			b.WriteByte('\n')
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	recalled, recalledRefs, err := memory.RecallWithReferences(ctx, tx, run, step, refs != nil)
	if err != nil {
		return "", nil, err
	}
	if refs != nil {
		*refs = append(*refs, recalledRefs...)
	}
	b.WriteString(recalled)
	return b.String(), files, nil
}

// UpdateSyncedText replaces only the previously published version in its original project.
func (o Objects) UpdateSyncedText(ctx context.Context, tx pgx.Tx, project, id, actor, name, text string, previous int) error {
	tag, err := tx.Exec(ctx, `update sources set current_version=current_version+1,name=$4
		where project_id=$1 and id=$2 and current_version=$3 and revoked_at is null`, project, id, previous, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := o.saveSourceVersion(ctx, tx, id, actor, name, previous+1, strings.NewReader(text), 256<<10,
		"text/plain; charset=utf-8", extraction{text: &text}); err != nil {
		return err
	}
	if err := recordSourceChange(ctx, tx, project, id, "source_updated"); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "source.save", id, map[string]int{"version": previous + 1})
}

// RevokeSyncedText revokes a connected source and invalidates dependent run inputs.
func (o Objects) RevokeSyncedText(ctx context.Context, tx pgx.Tx, project, id, actor string) error {
	tag, err := tx.Exec(ctx, `update sources set revoked_at=now()
		where project_id=$1 and id=$2 and revoked_at is null`, project, id)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := recordSourceChange(ctx, tx, project, id, "source_revoked"); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "source.revoke", id, map[string]bool{"revoked": true})
}
