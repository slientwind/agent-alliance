package knowledge

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type packageItem struct {
	Step               int             `json:"step"`
	Dependencies       json.RawMessage `json:"dependencies"`
	Artifact           string          `json:"artifact"`
	Name               string          `json:"name"`
	Version            int             `json:"version"`
	Reviewer           string          `json:"reviewer"`
	ReviewedAt         time.Time       `json:"reviewedAt"`
	TakeoverReviewedAt *time.Time      `json:"takeoverReviewedAt,omitempty"`
	TakeoverReviewer   *string         `json:"takeoverReviewer,omitempty"`
	Path               string          `json:"path"`
	Media              string          `json:"mediaType"`
	Digest             string          `json:"sha256"`
}

func (s Server) deliveryPackage(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	project := r.PathValue("id")
	checkMember := func() error {
		var member string
		return s.DB.QueryRow(r.Context(), "select m.member_id from project_members m join members u on u.id=m.member_id and u.enabled where m.project_id=$1 and m.member_id=$2", project, u.ID).Scan(&member)
	}
	if err := checkMember(); err != nil {
		return err
	}
	id, err := SelectDelivery(r, s.DB, project)
	if err != nil {
		return err
	}
	return WriteDeliveryPackage(w, r, s.DB, s.Objects, id, checkMember)
}

// A previously shared bare URL stays pinned to the first confirmed edition.
func DeliveryEdition(r *http.Request) (int, error) {
	raw, ok := r.URL.Query()["edition"]
	if !ok {
		return 1, nil
	}
	if len(raw) == 1 {
		n, err := strconv.ParseInt(raw[0], 10, 32)
		if err == nil && n > 0 {
			return int(n), nil
		}
	}
	return 0, web.Fail(422, "交付批次必须是正整数")
}
func SelectDelivery(r *http.Request, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, project string) (string, error) {
	edition, err := DeliveryEdition(r)
	if err != nil {
		return "", err
	}
	var id string
	err = db.QueryRow(r.Context(), "select id from deliveries where project_id=$1 and edition=$2", project, edition).Scan(&id)
	if err == pgx.ErrNoRows && edition == 1 {
		return "", web.Fail(409, "成果尚待最终确认")
	}
	return id, err
}

func DistributionError(state string) error {
	switch state {
	case "clear", "reference":
		return nil
	case "invalid":
		return web.Fail(409, "这批成果的结论需要修订，已暂停分发")
	case "restricted":
		return web.Fail(409, "这批成果包含分发受限的内容")
	default:
		return web.Fail(409, "资料授权已撤销，分发范围待核对")
	}
}
func deliveryDistribution(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (string, error) {
	var state string
	if err := db.QueryRow(ctx, "select distribution_state(id) from deliveries where id=$1", id).Scan(&state); err != nil {
		return "", err
	}
	return state, DistributionError(state)
}

func zipFilename(name string) string {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	base = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, base)
	if base == "" || base == "." || base == ".." || base == "/" {
		return "artifact"
	}
	return base
}

// WriteDeliveryPackage packages only final, immutable delivery items. Its callers
// first authorize either project membership or the external run credential.
func WriteDeliveryPackage(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, objects Objects, delivery string, revalidate func() error) error {
	pack, err := PrepareDeliveryPackage(r.Context(), db, objects, delivery)
	if err != nil {
		return err
	}
	defer pack.Close()
	if err := revalidate(); err != nil {
		return err
	}
	pack.distribution, err = deliveryDistribution(r.Context(), db, delivery)
	if err != nil {
		return err
	}
	pack.Serve(w, r)
	return nil
}

type DeliveryPackage struct {
	file         *os.File
	id           string
	confirmed    time.Time
	distribution string
}

func (pack *DeliveryPackage) Close() { pack.file.Close(); os.Remove(pack.file.Name()) }
func (pack *DeliveryPackage) Serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=delivery-%s.zip", pack.id))
	w.Header().Set("X-Delivery-ID", pack.id)
	w.Header().Set("X-Delivery-Distribution", pack.distribution)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "delivery.zip", pack.confirmed, pack.file)
}

// Accept a transaction so the archive can be verified before a temporary reviewer's access ends.
func PrepareDeliveryPackage(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, objects Objects, delivery string) (result *DeliveryPackage, failure error) {
	distribution, err := deliveryDistribution(ctx, db, delivery)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		ID           string            `json:"delivery"`
		Edition      int               `json:"edition"`
		Previous     *string           `json:"previous,omitempty"`
		Changes      json.RawMessage   `json:"changes"`
		ChangedNotes json.RawMessage   `json:"changedNotes"`
		Handoff      json.RawMessage   `json:"handoff"`
		IsPartial    bool              `json:"isPartial"`
		Coverage     json.RawMessage   `json:"coverage,omitempty"`
		ConfirmedBy  string            `json:"confirmedBy"`
		Assignment   *string           `json:"reviewerAssignment,omitempty"`
		ConfirmedAt  time.Time         `json:"confirmedAt"`
		Sources      []json.RawMessage `json:"sources,omitempty"`
		Items        []packageItem     `json:"items"`
	}
	err = db.QueryRow(ctx, `select d.id,d.confirmed_by,d.created_at,d.reviewer_assignment,d.handoff,c.edition,c.previous,to_jsonb(c.changes),to_jsonb(c.changed_notes),d.partial,d.coverage from deliveries d join delivery_comparisons c on c.id=d.id where d.id=$1`, delivery).Scan(&manifest.ID, &manifest.ConfirmedBy, &manifest.ConfirmedAt, &manifest.Assignment, &manifest.Handoff, &manifest.Edition, &manifest.Previous, &manifest.Changes, &manifest.ChangedNotes, &manifest.IsPartial, &manifest.Coverage)
	if err != nil {
		return nil, err
	}
	sourceRows, err := db.Query(ctx, `select jsonb_build_object('id',s.source_id,'pinned',s.pinned,'observed',s.observed,'revoked',s.revoked,'assessments',s.assessments,'revisionInputs',s.revision_inputs) || jsonb_strip_nulls(jsonb_build_object('calculationReceipt',s.calculation_receipt,'lookupReceipt',s.lookup_receipt,'stepReceipts',nullif(s.step_receipts,'[]'::jsonb)))
 from delivery_source_evidence s where s.delivery_id=$1 order by coalesce((s.calculation_receipt->>'plan')::integer,(s.lookup_receipt->>'plan')::integer,0),s.source_id`, manifest.ID)
	if err != nil {
		return nil, err
	}
	manifest.Sources, err = pgx.CollectRows(sourceRows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `select i.ordinal,i.artifact_id,i.name,i.dependencies,v.version,v.body,rev.reviewer,rev.created_at,tr.created_at,tr.reviewer,f.filename,f.media_type,f.object_hash
	from delivery_recipient_items i
	join artifact_versions v on (v.artifact_id,v.version)=(i.artifact_id,i.version)
	join reviews rev on (rev.artifact_id,rev.version)=(i.artifact_id,i.version)
	left join takeover_reviews tr on (tr.artifact_id,tr.version)=(i.artifact_id,i.version) and tr.assignment=$2
 left join artifact_files f on (f.artifact_id,f.version)=(i.artifact_id,i.version)
	where i.delivery_id=$1 order by i.ordinal`, manifest.ID, manifest.Assignment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Build the archive before committing an HTTP success, so a missing blob
	// or interrupted read cannot produce a successful but partial delivery.
	file, err := os.CreateTemp("", "alliance-delivery-*.zip")
	if err != nil {
		return nil, err
	}
	defer func() {
		if failure != nil {
			file.Close()
			os.Remove(file.Name())
		}
	}()
	zipper := zip.NewWriter(file)
	for rows.Next() {
		var item packageItem
		var body string
		var filename, media, hash *string
		if err := rows.Scan(&item.Step, &item.Artifact, &item.Name, &item.Dependencies, &item.Version, &body, &item.Reviewer, &item.ReviewedAt, &item.TakeoverReviewedAt, &item.TakeoverReviewer, &filename, &media, &hash); err != nil {
			return nil, err
		}
		name := item.Name + ".txt"
		item.Media = "text/plain; charset=utf-8"
		if filename != nil {
			name = *filename
			item.Media = *media
		}
		item.Path = fmt.Sprintf("artifacts/%02d-v%d-%s", item.Step, item.Version, zipFilename(name))
		output, err := zipper.Create(item.Path)
		if err != nil {
			return nil, err
		}
		digest := sha256.New()
		dst := io.MultiWriter(output, digest)
		if hash != nil {
			blob, err := objects.Open(*hash)
			if err != nil {
				slog.Error("delivery original unavailable", "delivery", manifest.ID, "artifact", item.Artifact, "version", item.Version, "error", err)
				return nil, web.Fail(503, "成果原件暂时无法读取，请联系管理员恢复后重试")
			}
			_, err = io.Copy(dst, blob)
			blob.Close()
			if err != nil {
				return nil, err
			}
		} else if _, err = io.WriteString(dst, body); err != nil {
			return nil, err
		}
		item.Digest = hex.EncodeToString(digest.Sum(nil))
		if hash != nil && item.Digest != *hash {
			slog.Error("delivery original failed checksum", "delivery", manifest.ID, "artifact", item.Artifact, "version", item.Version)
			return nil, web.Fail(503, "成果原件校验失败，请联系管理员恢复后重试")
		}
		manifest.Items = append(manifest.Items, item)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(manifest.Items) == 0 {
		return nil, pgx.ErrNoRows
	}
	entry, err := zipper.Create("manifest.json")
	if err != nil {
		return nil, err
	}
	if err = json.NewEncoder(entry).Encode(manifest); err != nil {
		return nil, err
	}
	if err = zipper.Close(); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return &DeliveryPackage{file: file, id: manifest.ID, distribution: distribution, confirmed: manifest.ConfirmedAt}, nil
}
