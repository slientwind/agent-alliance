package policy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DLPAdmin struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

func (a DLPAdmin) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/governance/dlp/rules", auth.Protect(true, a.list))
	mux.Handle("PUT /api/admin/governance/dlp/rules/{id}", auth.Protect(true, a.configure))
	mux.Handle("POST /api/admin/governance/dlp/inspect", auth.Protect(true, a.inspect))
}

type dlpRuleView struct {
	ID          string      `json:"id"`
	Kind        DLPKind     `json:"kind"`
	Name        string      `json:"name"`
	Sensitivity Sensitivity `json:"sensitivity"`
	Enabled     bool        `json:"enabled"`
	Revision    int         `json:"revision"`
}

func dlpRuleScope(id string) string { return "enterprise/dlp/rule/" + id }

func (a DLPAdmin) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := a.DB.Query(r.Context(), "select id,kind,name,sensitivity,enabled,revision from dlp_rules order by name,id")
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dlpRuleView])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (a DLPAdmin) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	raw := r.PathValue("id")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || raw != id.String() {
		return web.Fail(404, "找不到脱敏规则")
	}
	var in struct {
		Kind        DLPKind     `json:"kind"`
		Name        string      `json:"name"`
		Sensitivity Sensitivity `json:"sensitivity"`
		Enabled     *bool       `json:"enabled"`
		Match       *string     `json:"match"`
		Expected    int         `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || strings.ContainsFunc(in.Name, unicode.IsControl) ||
		in.Enabled == nil || in.Expected < 0 {
		return web.Fail(422, "请填写规则名称、状态和当前版本")
	}
	ctx := r.Context()
	var revision int
	err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(17952118)"); err != nil {
			return err
		}
		var encrypted []byte
		var current int
		err := tx.QueryRow(ctx, "select revision,encrypted_match from dlp_rules where id=$1 for update", raw).Scan(&current, &encrypted)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.Expected != current {
			return web.Fail(409, "脱敏规则已更新，请刷新")
		}
		if current == 0 {
			var n int
			if err := tx.QueryRow(ctx, "select count(*) from dlp_rules").Scan(&n); err != nil {
				return err
			}
			if n >= MaxDLPRules {
				return web.Fail(409, "脱敏规则已达到数量上限")
			}
		}
		match := ""
		if in.Match != nil {
			match = strings.TrimSpace(*in.Match)
		} else if len(encrypted) != 0 {
			match, err = a.Vault.Open(encrypted, dlpRuleScope(raw))
			if err != nil {
				return err
			}
		}
		if _, err := NewDLPScanner([]DLPRule{{Kind: in.Kind, Match: match, Sensitivity: in.Sensitivity}}); err != nil {
			return web.Fail(422, "请选择密级并填写有效的敏感词或底价字段")
		}
		if in.Match != nil {
			encrypted, err = a.Vault.Seal(match, dlpRuleScope(raw))
			if err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `insert into dlp_rules(id,kind,name,sensitivity,encrypted_match,enabled)
			values($1,$2,$3,$4,$5,$6) on conflict(id) do update set kind=excluded.kind,name=excluded.name,
			sensitivity=excluded.sensitivity,encrypted_match=excluded.encrypted_match,enabled=excluded.enabled,
			revision=dlp_rules.revision+1,updated_at=clock_timestamp() returning revision`,
			raw, in.Kind, in.Name, in.Sensitivity, encrypted, *in.Enabled).Scan(&revision)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "dlp.rule.configure", raw, map[string]any{
			"kind": in.Kind, "sensitivity": in.Sensitivity, "enabled": *in.Enabled, "revision": revision,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func loadDLPScanner(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, vault resources.Vault) (DLPScanner, error) {
	rows, err := q.Query(ctx, "select id,kind,sensitivity,encrypted_match from dlp_rules where enabled order by id")
	if err != nil {
		return DLPScanner{}, err
	}
	defer rows.Close()
	rules := make([]DLPRule, 0)
	for rows.Next() {
		var id string
		var cipher []byte
		var rule DLPRule
		if err := rows.Scan(&id, &rule.Kind, &rule.Sensitivity, &cipher); err != nil {
			return DLPScanner{}, err
		}
		rule.Match, err = vault.Open(cipher, dlpRuleScope(id))
		if err != nil {
			return DLPScanner{}, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return DLPScanner{}, err
	}
	return NewDLPScanner(rules)
}

func (a DLPAdmin) inspect(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var in struct {
		Field       string      `json:"field"`
		Sample      string      `json:"sample"`
		Sensitivity Sensitivity `json:"sensitivity"`
	}
	if err := web.ReadLimited(w, r, &in, 256<<10); err != nil {
		return err
	}
	if (in.Sensitivity != "" && sensitivityRank(in.Sensitivity) == 0) || len(in.Field) > 256 {
		return web.Fail(422, "请核对字段名与密级")
	}
	scanner, err := loadDLPScanner(r.Context(), a.DB, a.Vault)
	if err != nil {
		return err
	}
	findings := scanner.Scan(in.Field, in.Sample, in.Sensitivity)
	if len(findings) == 1 && findings[0].Start == 0 && findings[0].End == len(in.Sample) &&
		(findings[0].Kind == DLPFloorPrice || findings[0].Kind == DLPLabel) {
		// A field-wide disposition still takes precedence on egress. The sample
		// inspector also reports the independent rules matched by the same text.
		classification := findings[0]
		for _, match := range scanner.Scan("", in.Sample, "") {
			if match != classification {
				findings = append(findings, match)
			}
		}
	}
	if findings == nil {
		findings = []DLPFinding{}
	}
	return web.JSON(w, http.StatusOK, map[string][]DLPFinding{"findings": findings})
}
