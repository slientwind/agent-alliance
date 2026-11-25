package knowledgesync

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const knowledgeCitationPath = "/api/spaces/{space}/knowledge/documents/{document}/citations/{version}/{ordinal}"

func (s Server) citationRoutes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET "+knowledgeCitationPath, auth.Protect(false, s.readCitation))
	mux.Handle("GET "+knowledgeCitationPath+"/file", auth.Protect(false, s.readCitationFile))
}

func citationKey(r *http.Request) (string, knowledge.KnowledgeCitationKey, error) {
	space, doc := r.PathValue("space"), r.PathValue("document")
	version, first := strconv.Atoi(r.PathValue("version"))
	ordinal, second := strconv.Atoi(r.PathValue("ordinal"))
	if !validID(space) || !validID(doc) || first != nil || second != nil || version < 1 || ordinal < 1 || ordinal > 50000 || version > 2147483647 {
		return "", knowledge.KnowledgeCitationKey{}, web.Fail(404, "找不到知识出处")
	}
	return space, knowledge.KnowledgeCitationKey{Document: doc, Version: version, Ordinal: ordinal}, nil
}

func (s Server) readCitation(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, key, err := citationKey(r)
	if err != nil {
		return err
	}
	matched, err := knowledge.ResolveKnowledgeEvidence(r.Context(), s.DB, space, actor.ID, []knowledge.KnowledgeCitationKey{key})
	if err != nil {
		return err
	}
	if len(matched) != 1 {
		return web.Fail(404, "知识出处已不可用")
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, struct {
		knowledge.KnowledgeEvidence
		Ref  string `json:"ref"`
		File string `json:"fileUrl"`
	}{matched[0], key.Label(), fmt.Sprintf("/api/spaces/%s/knowledge/documents/%s/citations/%d/%d/file", space, key.Document, key.Version, key.Ordinal)})
}

func (s Server) readCitationFile(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, key, err := citationKey(r)
	if err != nil {
		return err
	}
	var hash, media, name string
	err = s.DB.QueryRow(r.Context(), `select v.object_hash,v.media_type,d.title from kb_retrieval_documents($1,$2) a
	 join kb_documents d on d.id=a.document_id
	 join kb_document_versions v on (v.document_id,v.version)=(a.document_id,a.content_version)
	 join kb_chunks k on (k.document_id,k.version)=(v.document_id,v.version)
	 where (d.id,v.version,k.ordinal)=($3,$4,$5)`, space, actor.ID, key.Document, key.Version, key.Ordinal).Scan(&hash, &media, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "知识原文已不可用")
	}
	if err != nil {
		return err
	}
	file, err := s.Objects.Open(hash)
	if errors.Is(err, os.ErrNotExist) {
		return web.Fail(404, "知识原文已不可用")
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	disposition, format := "attachment", parserFormat(media)
	switch media {
	case "application/pdf":
		disposition = "inline"
	case "text/plain", "text/markdown":
		disposition, format, media = "inline", "txt", "text/plain; charset=utf-8"
	case "text/html":
		media = "application/octet-stream"
	}
	name = strings.NewReplacer("/", " ", "\\", " ", "\r", "", "\n", "").Replace(name)
	if format != "" && !strings.HasSuffix(strings.ToLower(name), "."+format) {
		name += "." + format
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, name, info.ModTime(), file)
	return nil
}
