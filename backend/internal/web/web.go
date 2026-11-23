package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

type Problem struct {
	Status  int
	Message string
}

func (p Problem) Error() string             { return p.Message }
func Fail(status int, message string) error { return Problem{status, message} }
func Read(r *http.Request, dst any) error {
	return readJSON(io.LimitReader(r.Body, 2<<20), dst)
}

func ReadLimited(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	return readJSON(http.MaxBytesReader(w, r.Body, limit), dst)
}

func readJSON(input io.Reader, dst any) error {
	dec := json.NewDecoder(input)
	dec.DisallowUnknownFields()
	var tooLarge *http.MaxBytesError
	if err := dec.Decode(dst); err != nil {
		if errors.As(err, &tooLarge) {
			return Fail(413, "请求超过允许大小")
		}
		return Fail(422, "请求格式不正确")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if errors.As(err, &tooLarge) {
			return Fail(413, "请求超过允许大小")
		}
		return Fail(422, "请求包含多余内容")
	}
	return nil
}
func JSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

type Handler func(http.ResponseWriter, *http.Request) error

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := Token()
	w.Header().Set("X-Request-ID", id)
	if err := h(w, r); err != nil {
		var p Problem
		var db *pgconn.PgError
		switch {
		case errors.As(err, &p):
		case errors.Is(err, pgx.ErrNoRows):
			p = Problem{404, "对象不存在或无权访问"}
		case errors.As(err, &db) && db.Code == "23505":
			p = Problem{409, "记录已存在"}
		case errors.As(err, &db) && db.Code == "22P02":
			p = Problem{422, "标识或数据格式无效"}
		default:
			slog.Error("request failed", "request", id, "error_type", fmtErrorType(err))
			p = Problem{500, "操作失败，请使用请求编号排查"}
		}
		_ = JSON(w, p.Status, map[string]any{"error": p.Message, "requestId": id})
	}
}
func fmtErrorType(err error) string {
	var db *pgconn.PgError
	if errors.As(err, &db) {
		return "postgres:" + db.Code
	}
	return "internal"
}
func Token() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Hash(token string) []byte { s := sha256.Sum256([]byte(token)); return s[:] }
func Blank(s string) bool      { return strings.TrimSpace(s) == "" }
