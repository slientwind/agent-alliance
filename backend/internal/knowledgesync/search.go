package knowledgesync

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
)

func (s Server) searchDocuments(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到知识空间")
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 512 || strings.ContainsRune(query, 0) {
		return web.Fail(422, "请提供 1–512 字的知识查询")
	}
	result, err := knowledge.SearchKnowledge(r.Context(), s.DB, s.Inference, space, actor.ID, query, 8)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, result)
}
