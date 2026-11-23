package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	encoderRevision = "bge-small-zh-v1.5-int8/1"
	embeddingBatch  = 16
)

var embeddingClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func embeddings(ctx context.Context, kind string, texts []string) ([]json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"kind": kind, "texts": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(os.Getenv("RETRIEVAL_URL"), "/")+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CONNECTOR_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	res, err := embeddingClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("检索模型不可用")
	}
	var out struct {
		Encoder string
		Vectors []json.RawMessage
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 256<<10)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Encoder != encoderRevision || len(out.Vectors) != len(texts) {
		return nil, errors.New("检索模型修订或数量不匹配")
	}
	for _, v := range out.Vectors {
		var values []float32
		if err := json.Unmarshal(v, &values); err != nil || len(values) != 512 {
			return nil, errors.New("检索向量维度不匹配")
		}
		var norm float64
		for _, x := range values {
			norm += float64(x) * float64(x)
		}
		if math.Abs(norm-1) > 0.01 {
			return nil, errors.New("检索向量未归一化")
		}
	}
	return out.Vectors, nil
}

type passage struct {
	Source          string
	Version, Offset int
	Text            string
}

type PinnedPassage struct {
	Source         string
	Version, Start int
	Text           string
	Fresh          bool
}

type SemanticLookup struct {
	Hits         []PinnedPassage
	IndexPending bool
}

// A mutable project search corpus cannot stand in for a confirmed run. Look
// up vectors only for exact readable run pins. Recheck the same scope after
// encoding so a concurrent withdrawal or delegation change excludes the row.
const pinnedSemanticScope = `from runs r
 join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,r.actor)
 join members actor on actor.id=r.actor and actor.enabled
 join plan_sources pin on (pin.project_id,pin.plan_version)=(r.project_id,r.plan_version)
 join run_source_inputs f on (f.run_id,f.source_id)=(r.id,pin.source_id)
 join source_passages p on (p.source_id,p.version)=(f.source_id,f.version)
 where r.id=$1 and pin.receipt_step is null and f.extracted is not null
 and not (f.filename ~* '\.md$' and p.passage ~ '^ {0,3}#{1,6}[[:space:]]')
 and (` + identity.RunGrantSQL + `)`

func SemanticLookupEnabled() bool { return os.Getenv("RETRIEVAL_URL") != "" }

func SourceIndexPending(ctx context.Context, tx pgx.Tx, run string) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `select exists(select 1 `+pinnedSemanticScope+` and p.encoder is distinct from $2)`, run, encoderRevision).Scan(&pending)
	return pending, err
}

func PinnedSemanticPassages(ctx context.Context, db *pgxpool.Pool, run, question string) (SemanticLookup, error) {
	var result SemanticLookup
	question = strings.Join(strings.Fields(question), " ")
	if !SemanticLookupEnabled() || question == "" || utf8.RuneCountInString(question) > 160 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `select p.source_id,p.version,p.start_char,p.passage `+pinnedSemanticScope+`
 and r.status='running' and p.encoder is distinct from $2 order by p.source_id,p.start_char limit $3`, run, encoderRevision, embeddingBatch+1)
	if err != nil {
		return result, err
	}
	pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[passage])
	if err != nil {
		return result, err
	}
	result.IndexPending = len(pending) > embeddingBatch
	type freshPassage struct {
		Source    string          `json:"source_id"`
		Version   int             `json:"version"`
		Start     int             `json:"start_char"`
		Text      string          `json:"passage"`
		Embedding json.RawMessage `json:"embedding"`
	}
	fresh := []freshPassage{}
	if len(pending) > 0 && !result.IndexPending {
		encoded, err := passageEmbeddings(ctx, pending)
		if err != nil {
			return result, err
		}
		for i, p := range pending {
			fresh = append(fresh, freshPassage{p.Source, p.Version, p.Offset, p.Text, encoded[i]})
		}
	} else {
		var available bool
		if err := db.QueryRow(ctx, `select exists(select 1 `+pinnedSemanticScope+` and r.status='running' and p.encoder=$2)`, run, encoderRevision).Scan(&available); err != nil || !available {
			return result, err
		}
	}
	// The run may use newly encoded text immediately. These vectors stay in
	// this attempt so the background indexer alone publishes durable rows.
	data, err := json.Marshal(fresh)
	if err != nil {
		return result, err
	}
	vectors, err := embeddings(ctx, "query", []string{question})
	if err != nil {
		return result, err
	}
	rows, err = db.Query(ctx, `with corpus as materialized (
 select p.source_id,p.version,p.start_char,p.passage,p.encoder,p.embedding `+pinnedSemanticScope+` and r.status='running'
), scored as (
 select p.source_id,p.version,p.start_char,p.passage,local.embedding is not null as fresh,
 1-(coalesce(local.embedding::text::vector,case when p.encoder=$2 then p.embedding end) <=> $3::text::vector) as similarity
 from corpus p left join jsonb_to_recordset($4::jsonb) as local(source_id uuid,version integer,start_char integer,passage text,embedding jsonb)
 using(source_id,version,start_char,passage)
 where p.encoder=$2 or local.embedding is not null
)
 select source_id,version,start_char,passage,fresh from scored where similarity>=0.55
 order by similarity desc,source_id,start_char limit 6`, run, encoderRevision, string(vectors[0]), data)
	if err != nil {
		return result, err
	}
	result.Hits, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PinnedPassage])
	return result, err
}

func passageEmbeddings(ctx context.Context, items []passage) ([]json.RawMessage, error) {
	texts := make([]string, len(items))
	for i, p := range items {
		texts[i] = p.Text
	}
	return embeddings(ctx, "passage", texts)
}

// Inference runs outside the transaction. A revoked or superseded passage
// disappears before write-back; an in-flight response can never recreate it.
func indexPassages(ctx context.Context, db *pgxpool.Pool) (int, error) {
	rows, err := db.Query(ctx, `select source_id,version,start_char,passage from source_passages
 where encoder is distinct from $1 order by source_id,version,start_char limit $2`, encoderRevision, embeddingBatch)
	if err != nil {
		return 0, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[passage])
	if err != nil || len(items) == 0 {
		return 0, err
	}
	vectors, err := passageEmbeddings(ctx, items)
	if err != nil {
		return 0, err
	}
	err = database.WithTx(ctx, db, func(tx pgx.Tx) error {
		for i, p := range items {
			if _, err := tx.Exec(ctx, `update source_passages set embedding=$1::text::vector,encoder=$2
 where source_id=$3 and version=$4 and start_char=$5 and passage=$6 and encoder is distinct from $2`, string(vectors[i]), encoderRevision, p.Source, p.Version, p.Offset, p.Text); err != nil {
				return err
			}
		}
		return nil
	})
	return len(items), err
}

func MaintainPassages(ctx context.Context, db *pgxpool.Pool) {
	if os.Getenv("RETRIEVAL_URL") == "" {
		return
	}
	for ctx.Err() == nil {
		count, err := indexPassages(ctx, db)
		delay := 3 * time.Second
		if err != nil {
			log.Print("资料向量索引暂不可用，将重试")
			delay = 15 * time.Second
		} else if count == embeddingBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// Both rankings and coverage are computed from the same current, project-scoped
// corpus. An exact occurrence precedes candidates from reciprocal rank fusion.
const retrievalSQL = `with corpus as materialized (
 select s.id,s.name,p.version,p.start_char,p.passage,p.embedding,p.encoder
 from project_members pm join sources s on s.project_id=pm.project_id
 join source_passages p on (p.source_id,p.version)=(s.id,s.current_version)
 where pm.member_id=$1 and pm.project_id=$2 and s.revoked_at is null
), scored as (
 select *,strpos(lower(passage),lower($3)) as pos,word_similarity(lower($3),lower(passage)) as lexical,
 (name ~* '\.md$' and passage ~ '^ {0,3}#{1,6}[[:space:]]') as heading,
 case when encoder=$5 then 1-(embedding <=> $4::text::vector) end as semantic from corpus
), lexical as (
 select id,start_char,row_number() over(order by (pos>0) desc,lexical desc,id,start_char) as rank
 from scored where pos>0 or (not heading and lexical>=0.25) order by rank limit 20
), semantic as (
 select id,start_char,row_number() over(order by semantic desc,id,start_char) as rank
 from scored where not heading and semantic>=0.55 order by rank limit 20
), candidates as (
 select s.*,l.rank as lex_rank,v.rank as sem_rank,
 coalesce(1.0/(60+l.rank),0)+coalesce(1.0/(60+v.rank),0) as score
 from scored s left join lexical l using(id,start_char) left join semantic v using(id,start_char)
 where l.rank is not null or v.rank is not null order by (pos>0) desc,score desc,lex_rank nulls last,id,start_char limit 10
)
select jsonb_build_object('passages',(select count(*) from corpus),'indexed',(select count(*) from corpus where encoder=$5),
 'hits',coalesce((select jsonb_agg(jsonb_build_object('id',id,'name',name,'version',version,'start',start_char,
 'end',start_char+char_length(passage)-1,'excerpt',passage,'location',(select loc->>'label' from source_versions v cross join lateral jsonb_array_elements(v.extraction_locations) loc where (v.source_id,v.version)=(candidates.id,candidates.version) and candidates.start_char between (loc->>'start')::int and (loc->>'end')::int limit 1),'exact',pos>0,'lexical',lex_rank is not null,'semantic',sem_rank is not null)
 order by (pos>0) desc,score desc,lex_rank nulls last,id,start_char) from candidates),'[]'::jsonb))
where exists(select from project_members where project_id=$2 and member_id=$1)`

func (s Server) retrieve(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || utf8.RuneCountInString(query) > 160 {
		return web.Fail(422, "请输入 1 至 160 个字符")
	}
	project := r.PathValue("id")
	var member bool
	if err := s.DB.QueryRow(r.Context(), "select exists(select from project_members where project_id=$1 and member_id=$2)", project, u.ID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return web.Fail(404, "项目不可见")
	}
	mode := "lexical"
	var vec *string
	if os.Getenv("RETRIEVAL_URL") != "" {
		mode = "unavailable"
		v, err := embeddings(r.Context(), "query", []string{query})
		if err == nil {
			val := string(v[0])
			vec = &val
			mode = "hybrid"
		}
	}
	var result json.RawMessage
	if err := s.DB.QueryRow(r.Context(), retrievalSQL, u.ID, project, query, vec, encoderRevision).Scan(&result); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, map[string]any{"mode": mode, "result": result})
}
