package knowledgeradar

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) ServeConflicts(ctx context.Context) {
	var nextSweep time.Time
	for ctx.Err() == nil {
		space, err := s.scanStandards(ctx)
		if err == nil && space != "" {
			err = s.findConflicts(ctx, space)
		}
		if time.Now().After(nextSweep) && err == nil {
			nextSweep = time.Now().Add(time.Minute)
			err = s.findConflicts(ctx, "")
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("enterprise knowledge conflict scan deferred")
		}
		delay := 10 * time.Second
		if err == nil && space != "" {
			delay = 200 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s Server) scanStandards(ctx context.Context) (string, error) {
	var space, doc string
	var version int
	err := s.DB.QueryRow(ctx, `select d.space_id,d.id,d.content_version from kb_documents d
	 join kb_document_versions v on (v.document_id,v.version)=(d.id,d.content_version) and v.parse_status='ready'
	 left join kb_standard_scans s on (s.document_id,s.version)=(d.id,d.content_version)
	 where d.source_state='present' and d.doc_status='active' and coalesce(s.completed,false)=false
	 order by coalesce(s.scanned_at,v.created_at),d.id limit 1`).Scan(&space, &doc, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into kb_standard_scans(document_id,version) values($1,$2) on conflict do nothing`, doc, version); err != nil {
			return err
		}
		var offset int
		var done bool
		if err := tx.QueryRow(ctx, `select last_ordinal,completed from kb_standard_scans where (document_id,version)=($1,$2) for update`,
			doc, version).Scan(&offset, &done); err != nil || done {
			return err
		}
		const batchSize = 64
		rows, err := tx.Query(ctx, `select ordinal,body,breadcrumb from kb_chunks where (document_id,version)=($1,$2)
		 and ordinal>$3 order by ordinal limit $4`, doc, version, offset, batchSize)
		if err != nil {
			return err
		}
		chunks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Ordinal    int
			Body       string
			Breadcrumb []string
		}])
		if err != nil {
			return err
		}
		statements := &pgx.Batch{}
		for _, k := range chunks {
			offset = k.Ordinal
			for _, claim := range standardClaims(k.Body, k.Breadcrumb) {
				statements.Queue(`insert into kb_standard_claims(space_id,document_id,version,chunk_ordinal,metric,label,comparator,value,unit)
				 values($1,$2,$3,$4,$5,$6,$7,$8::text::numeric,$9) on conflict do nothing`, space, doc, version, k.Ordinal,
					claim.metric, claim.label, claim.comparator, claim.value, claim.unit)
			}
		}
		statements.Queue(`update kb_standard_scans set last_ordinal=$3,completed=$4,scanned_at=clock_timestamp()
		 where (document_id,version)=($1,$2)`, doc, version, offset, len(chunks) < batchSize)
		return tx.SendBatch(ctx, statements).Close()
	})
	return space, err
}

func (s Server) findConflicts(ctx context.Context, space string) error {
	_, err := s.DB.Exec(ctx, `with claims as materialized (
	 select id,space_id,document_id,metric,unit,comparator,value from kb_current_standard_claims
	 where ($1='' or space_id=nullif($1,'')::uuid)
	) insert into kb_conflict_alerts(space_id,left_claim,right_claim)
	 select a.space_id,a.id,b.id from claims a join claims b on (a.space_id,a.metric,a.unit)=(b.space_id,b.metric,b.unit)
	 where a.id<b.id and a.document_id<>b.document_id and (
	   (a.comparator=b.comparator and a.value<>b.value)
	   or (a.comparator='upper' and b.comparator in ('lower','exact') and a.value<b.value)
	   or (b.comparator='upper' and a.comparator in ('lower','exact') and b.value<a.value)
	   or (a.comparator='lower' and b.comparator='exact' and a.value>b.value)
	   or (b.comparator='lower' and a.comparator='exact' and b.value>a.value)
	 ) on conflict(left_claim,right_claim) do nothing`, space)
	return err
}

const visibleConflictJoins = `from kb_conflict_alerts a
 join kb_current_standard_claims l on (l.id,l.space_id)=(a.left_claim,a.space_id)
 join kb_current_standard_claims r on (r.id,r.space_id)=(a.right_claim,a.space_id)`

func (s Server) conflicts(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到制度冲突")
	}
	var data json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(item order by created_at desc,id),'[]') from (
	 select a.id,a.created_at,jsonb_build_object('id',a.id,'metric',l.label,'status',a.status,'revision',a.revision,
	 'resolution',a.resolution,'resolutionNote',a.resolution_note,
	 'left',jsonb_build_object('documentId',l.document_id,'version',l.version,'ordinal',l.chunk_ordinal,'title',dl.title,
	  'unit',l.unit,'value',rtrim(rtrim(l.value::text,'0'),'.'),'comparator',l.comparator,'citationAnchor',cl.citation_anchor),
	 'right',jsonb_build_object('documentId',r.document_id,'version',r.version,'ordinal',r.chunk_ordinal,'title',dr.title,
	  'unit',r.unit,'value',rtrim(rtrim(r.value::text,'0'),'.'),'comparator',r.comparator,'citationAnchor',cr.citation_anchor)) item
	 `+visibleConflictJoins+`
	 join kb_documents dl on dl.id=l.document_id join kb_documents dr on dr.id=r.document_id
	 join kb_chunks cl on (cl.document_id,cl.version,cl.ordinal)=(l.document_id,l.version,l.chunk_ordinal)
	 join kb_chunks cr on (cr.document_id,cr.version,cr.ordinal)=(r.document_id,r.version,r.chunk_ordinal)
	 where a.space_id=$1 and space_role(a.space_id,$2)='space_admin'
	 and kb_document_readable(l.document_id,$2) and kb_document_readable(r.document_id,$2)
	 order by a.created_at desc,a.id limit 150) x`, space, actor.ID).Scan(&data)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, data)
}

func (s Server) resolveConflict(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("alert")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到制度冲突")
	}
	var input struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
		Expected   int    `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &input, 4096); err != nil {
		return err
	}
	input.Note = strings.TrimSpace(input.Note)
	if (input.Resolution != "use_left" && input.Resolution != "use_right" && input.Resolution != "different_scope") || input.Expected < 1 ||
		input.Note == "" || utf8.RuneCountInString(input.Note) > 600 || strings.ContainsRune(input.Note, 0) {
		return web.Fail(422, "请选择处理口径并填写消歧原因")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, `select a.revision,a.status `+visibleConflictJoins+`
		 where (a.space_id,a.id)=($1,$2) and space_role(a.space_id,$3)='space_admin'
		 and kb_document_readable(l.document_id,$3) and kb_document_readable(r.document_id,$3) for update of a`, space, id, actor.ID).Scan(&revision, &state)
		if err != nil {
			return err
		}
		if revision != input.Expected || state != "open" {
			return web.Fail(409, "冲突已处理，请刷新")
		}
		if err := tx.QueryRow(ctx, `update kb_conflict_alerts set status='resolved',resolution=$2,resolution_note=$3,resolved_by=$4,
		 resolved_at=clock_timestamp(),revision=revision+1 where id=$1 returning revision`, id, input.Resolution, input.Note, actor.ID).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.conflict.resolve", id,
			map[string]any{"space": space, "resolution": input.Resolution, "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可消歧的现行制度")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (s Server) conflictRoutes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{space}/knowledge/conflicts", auth.Protect(false, s.conflicts))
	mux.Handle("PUT /api/spaces/{space}/knowledge/conflicts/{alert}/resolution", auth.Protect(false, s.resolveConflict))
}
