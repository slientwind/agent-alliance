package knowledgesync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
)

const layoutOutputLimit = 32 << 20

type layoutJob struct {
	document, media, hash, lease string
	version                      int
	size                         int64
}

type layoutChunk struct {
	Body       string          `json:"body"`
	Breadcrumb []string        `json:"breadcrumb"`
	Anchor     json.RawMessage `json:"anchor"`
}

func parserFormat(media string) string {
	switch media {
	case "application/pdf":
		return "pdf"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "text/html":
		return "html"
	case "text/csv":
		return "csv"
	}
	return ""
}

func (s Server) ServeLayout(ctx context.Context) {
	base := strings.TrimRight(os.Getenv("KB_PARSE_URL"), "/")
	token := os.Getenv("CONNECTOR_TOKEN")
	if base == "" || token == "" {
		return
	}
	client := &http.Client{Timeout: 85 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for ctx.Err() == nil {
		worked, err := s.parseNext(ctx, client, base, token)
		if err != nil && ctx.Err() == nil {
			slog.Warn("knowledge layout parsing deferred")
		}
		delay := 3 * time.Second
		if worked && err == nil {
			delay = 100 * time.Millisecond
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

func (s Server) parseNext(ctx context.Context, client *http.Client, base, token string) (bool, error) {
	var work layoutJob
	err := s.DB.QueryRow(ctx, `with ready as (
	 select v.document_id,v.version,o.size_bytes from kb_document_versions v
	 join kb_documents d on (v.document_id,v.version)=(d.id,d.content_version)
	 join kb_collections c on c.id=d.collection_id and c.enabled
	 join objects o on o.hash=v.object_hash
	 where d.source_state='present' and v.parsed_at is null and v.parse_status in ('pending','parsing','unavailable')
	 and v.parse_next_at<=clock_timestamp() order by v.parse_next_at,v.document_id,v.version limit 1 for update of v skip locked
	) update kb_document_versions v set parse_lease=gen_random_uuid(),parse_status='parsing',parse_next_at=clock_timestamp()+interval '100 seconds'
	 from ready r where (v.document_id,v.version)=(r.document_id,r.version)
	 returning v.document_id,v.version,v.object_hash,v.parse_lease,v.media_type,r.size_bytes`).Scan(&work.document, &work.version,
		&work.hash, &work.lease, &work.media, &work.size)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	format := parserFormat(work.media)
	if format == "" || work.size < 1 || work.size > maxKnowledgeFile {
		return true, s.deferLayout(ctx, work, "invalid")
	}
	file, err := s.Objects.Open(work.hash)
	if err != nil {
		return true, s.deferLayout(ctx, work, "unavailable")
	}
	defer file.Close()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/extract/"+format, file)
	if err != nil {
		return true, s.deferLayout(ctx, work, "unavailable")
	}
	r.ContentLength = work.size
	r.Header.Set("Content-Type", "application/octet-stream")
	bearer(r, token)
	res, err := client.Do(r)
	if err != nil {
		return true, s.deferLayout(ctx, work, "unavailable")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnprocessableEntity:
		return true, s.deferLayout(ctx, work, "invalid")
	case http.StatusRequestEntityTooLarge:
		return true, s.deferLayout(ctx, work, "limited")
	case http.StatusOK:
		if res.ContentLength < 0 || res.ContentLength > layoutOutputLimit || res.Header.Get("Content-Type") != "application/x-ndjson" {
			return true, s.deferLayout(ctx, work, "unavailable")
		}
	default:
		return true, s.deferLayout(ctx, work, "unavailable")
	}
	err = s.publishLayout(ctx, work, res.Body)
	if errors.Is(err, errInvalid) {
		return true, s.deferLayout(ctx, work, "invalid")
	}
	return true, err
}

func (s Server) deferLayout(ctx context.Context, job layoutJob, status string) error {
	_, err := s.DB.Exec(ctx, `update kb_document_versions set parse_lease=null,parse_status=$4,parse_next_at=clock_timestamp()+interval '1 minute'
	 where (document_id,version,parse_lease)=($1,$2,$3)`, job.document, job.version, job.lease, status)
	return err
}

func (s Server) publishLayout(ctx context.Context, job layoutJob, source io.Reader) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `select (d.content_version=v.version and d.source_state='present')
		 from kb_document_versions v join kb_documents d on d.id=v.document_id
		 where (v.document_id,v.version,v.parse_lease)=($1,$2,$3) for update of v`, job.document, job.version, job.lease).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from kb_chunks where (document_id,version)=($1,$2)", job.document, job.version); err != nil {
			return err
		}
		limited := &io.LimitedReader{R: source, N: layoutOutputLimit + 1}
		scanner := bufio.NewScanner(limited)
		scanner.Buffer(make([]byte, 8192), 112<<10)
		ordinal := 0
		var formatError bool
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"kb_chunks"}, []string{"document_id", "version", "ordinal", "body", "breadcrumb", "citation_anchor"},
			pgx.CopyFromFunc(func() ([]any, error) {
				if !scanner.Scan() {
					if limited.N == 0 {
						formatError = true
						return nil, errInvalid
					}
					return nil, scanner.Err()
				}
				var item layoutChunk
				if json.Unmarshal(scanner.Bytes(), &item) != nil || !item.valid() || ordinal >= 50000 {
					formatError = true
					return nil, errInvalid
				}
				ordinal++
				return []any{job.document, job.version, ordinal, item.Body, item.Breadcrumb, item.Anchor}, nil
			}))
		if formatError {
			return errInvalid
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update kb_document_versions set parse_status='ready',parsed_at=clock_timestamp(),parse_lease=null
		 where (document_id,version)=($1,$2)`, job.document, job.version)
		return err
	})
}

func (c layoutChunk) valid() bool {
	if strings.TrimSpace(c.Body) == "" || utf8.RuneCountInString(c.Body) > 16000 || strings.ContainsRune(c.Body, 0) ||
		c.Breadcrumb == nil || len(c.Breadcrumb) > 12 || len(c.Anchor) > 4096 {
		return false
	}
	for _, heading := range c.Breadcrumb {
		if !validRemoteID(heading, 880) || utf8.RuneCountInString(heading) > 220 {
			return false
		}
	}
	var anchor struct {
		Kind, Sheet                                string
		Page, EndPage, Paragraph, StartRow, EndRow int
	}
	if json.Unmarshal(c.Anchor, &anchor) != nil || anchor.EndPage < 0 || (anchor.EndPage > 0 && anchor.EndPage < anchor.Page) {
		return false
	}
	switch anchor.Kind {
	case "pdf", "slide":
		return anchor.Page > 0
	case "pdf_table", "slide_table":
		return anchor.Page > 0 && anchor.StartRow > 0 && anchor.EndRow >= anchor.StartRow
	case "sheet":
		return validRemoteID(anchor.Sheet, 128) && anchor.StartRow > 0 && anchor.EndRow >= anchor.StartRow
	case "csv", "word_table", "html_table":
		return anchor.StartRow > 0 && anchor.EndRow >= anchor.StartRow
	case "paragraph":
		return anchor.Paragraph > 0
	}
	return false
}
