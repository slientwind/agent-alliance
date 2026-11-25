package knowledge

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

// KnowledgeLexemes keeps complete Latin model numbers and generates overlapping
// words for scripts that do not normally separate words with spaces.
func KnowledgeLexemes(input string) []string {
	runes := []rune(strings.ToLower(norm.NFKC.String(input)))
	var result []string
	for i := 0; i < len(runes); {
		value := runes[i]
		if knowledgeCJK(value) {
			result = append(result, string(value))
			if i+1 < len(runes) && knowledgeCJK(runes[i+1]) {
				result = append(result, string(runes[i:i+2]))
			}
			i++
			continue
		}
		if !alphanumeric(value) {
			i++
			continue
		}
		start := i
		for i++; i < len(runes); i++ {
			char := runes[i]
			if alphanumeric(char) && !knowledgeCJK(char) {
				continue
			}
			if strings.ContainsRune("-_.:/", char) && i+1 < len(runes) && alphanumeric(runes[i+1]) && !knowledgeCJK(runes[i+1]) {
				continue
			}
			break
		}
		if i-start <= 100 {
			result = append(result, string(runes[start:i]))
		}
	}
	return result
}

func knowledgeCJK(value rune) bool {
	return unicode.In(value, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func alphanumeric(value rune) bool { return unicode.IsLetter(value) || unicode.IsDigit(value) }

func boundedKnowledgeText(text string, limit int) string {
	values := []rune(strings.TrimSpace(text))
	if len(values) > limit {
		return string(values[:limit])
	}
	return string(values)
}

type knowledgeIndexChunk struct {
	Document, Body string
	Version, Entry int
	Breadcrumb     []string
}

func maintainKnowledgeLexemes(ctx context.Context, db *pgxpool.Pool) (int, error) {
	rows, err := db.Query(ctx, `select k.document_id,k.body,k.version,k.ordinal,k.breadcrumb from kb_chunks k
	 join kb_documents d on (d.id,d.content_version)=(k.document_id,k.version)
	 join kb_document_versions v on (v.document_id,v.version)=(k.document_id,k.version) and v.parse_status='ready'
	 where k.lexemes is null and d.source_state='present' order by k.document_id,k.version,k.ordinal limit 48`)
	if err != nil {
		return 0, err
	}
	chunks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[knowledgeIndexChunk])
	if err != nil || len(chunks) == 0 {
		return 0, err
	}
	batches := &pgx.Batch{}
	for _, chunk := range chunks {
		words := KnowledgeLexemes(strings.Join(chunk.Breadcrumb, " ") + " " + chunk.Body)
		lexemes := make(map[string]int, len(words))
		for _, word := range words {
			lexemes[word]++
		}
		batches.Queue(`update kb_chunks set lexemes=$4,token_count=$5 where (document_id,version,ordinal)=($1,$2,$3) and lexemes is null`,
			chunk.Document, chunk.Version, chunk.Entry, lexemes, len(words))
	}
	return len(chunks), database.WithTx(ctx, db, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batches).Close() })
}

func maintainKnowledgeVectors(ctx context.Context, db *pgxpool.Pool, inference KBInference, current string) (int, error) {
	rows, err := db.Query(ctx, `select k.document_id,k.body,k.version,k.ordinal,k.breadcrumb from kb_chunks k
	 join kb_documents d on (d.id,d.content_version)=(k.document_id,k.version)
	 join kb_document_versions v on (v.document_id,v.version)=(k.document_id,k.version) and v.parse_status='ready'
	 where k.encoder is distinct from $1 and d.source_state='present' order by k.document_id,k.version,k.ordinal limit 8`, current)
	if err != nil {
		return 0, err
	}
	chunks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[knowledgeIndexChunk])
	if err != nil || len(chunks) == 0 {
		return 0, err
	}
	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		texts[i] = boundedKnowledgeText(strings.Join(chunk.Breadcrumb, " > ")+"\n"+chunk.Body, 2400)
	}
	encoder, vectors, err := inference.Embed(ctx, texts)
	if err != nil || encoder != current || len(vectors) != len(chunks) {
		return 0, errKBInference
	}
	batches := &pgx.Batch{}
	for i, chunk := range chunks {
		batches.Queue(`update kb_chunks set embedding=$4::text::vector,encoder=$5 where (document_id,version,ordinal)=($1,$2,$3)
		 and encoder is distinct from $5`, chunk.Document, chunk.Version, chunk.Entry, string(vectors[i]), encoder)
	}
	return len(chunks), database.WithTx(ctx, db, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batches).Close() })
}

func ServeKnowledgeIndex(ctx context.Context, db *pgxpool.Pool, inference KBInference) {
	var nextHealth time.Time
	var encoder string
	for ctx.Err() == nil {
		words, err := maintainKnowledgeLexemes(ctx, db)
		if err != nil && ctx.Err() == nil {
			slog.Warn("knowledge lexeme indexing deferred")
		}
		vectors := 0
		if inference != nil {
			if time.Now().After(nextHealth) {
				nextHealth = time.Now().Add(30 * time.Second)
				query, stop := context.WithTimeout(ctx, 2*time.Second)
				encoder, _ = inference.EmbeddingRevision(query)
				stop()
			}
			if encoder != "" {
				mapped, err := maintainKnowledgeVectors(ctx, db, inference, encoder)
				if err != nil {
					encoder = ""
				}
				vectors = mapped
			}
		}
		delay := 3 * time.Second
		if words > 0 || vectors > 0 {
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
