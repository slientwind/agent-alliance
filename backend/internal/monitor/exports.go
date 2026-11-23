package monitor

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

//go:embed financial_export.sql
var financialExportQuery string

type exportInput struct {
	From, To           time.Time
	Recipient, Purpose string
	Costs              bool
	CostLines          bool `json:",omitempty"`
}
type exportReceipt struct {
	ID      string    `json:"id"`
	Expires time.Time `json:"expiresAt"`
}

func (s Server) createExport(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in exportInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.From, in.To = in.From.UTC(), in.To.UTC()
	in.Recipient, in.Purpose = strings.TrimSpace(in.Recipient), strings.TrimSpace(in.Purpose)
	if in.From.IsZero() || !in.To.After(in.From) || in.To.Sub(in.From) > 366*24*time.Hour || in.Recipient == "" || in.Purpose == "" || len(in.Recipient) > 1000 || len(in.Purpose) > 2000 {
		return web.Fail(422, "请核对统计时间并填写接收对象和使用目的")
	}
	if in.CostLines && !in.Costs {
		return web.Fail(422, "逐笔费用文件须明确选择费用字段")
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return web.Fail(422, "缺少导出请求标识")
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return err
	}
	hash := web.Hash(string(encoded))
	ctx := r.Context()
	grant := r.PathValue("id")
	var receipt exportReceipt
	status := http.StatusCreated
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var kind, name string
		var allowed, costs bool
		err := tx.QueryRow(ctx, `select n.kind,n.name,case when $3 then g.financial_export else g.allow_export end,g.include_costs from management_grants g
			join members m on m.id=g.member_id and m.enabled join management_grant_names n on n.id=g.id where g.id=$1 and g.member_id=$2
			and g.revoked_at is null and g.expires_at>clock_timestamp() for update of g for share of m`, grant, u.ID, in.CostLines).Scan(&kind, &name, &allowed, &costs)
		if err != nil {
			return err
		}
		if !allowed || (in.Costs && !costs) {
			return web.Fail(403, "当前授权未开放所选字段的导出")
		}
		var stored []byte
		var usable bool
		err = tx.QueryRow(ctx, `select id,expires_at,request_hash,payload is not null and expires_at>clock_timestamp()
			from management_exports where grant_id=$1 and request_key=$2`, grant, key).Scan(&receipt.ID, &receipt.Expires, &stored, &usable)
		if err == nil {
			if !bytes.Equal(hash, stored) {
				return web.Fail(409, "同一导出请求的范围、字段或用途已变化")
			}
			if !usable {
				return web.Fail(410, "该文件已失效，请在当前授权下重新生成")
			}
			status = http.StatusOK
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var snapshot json.RawMessage
		if in.CostLines {
			if err = tx.QueryRow(ctx, financialExportQuery, grant, u.ID, in.From, in.To, kind, name).Scan(&snapshot); err != nil {
				return err
			}
			if len(snapshot) == 0 {
				return web.Fail(422, "本次超过一万笔调用，请缩短导出起止时间")
			}
			err = tx.QueryRow(ctx, `insert into management_exports(grant_id,request_key,request_hash,include_costs,cost_lines,expires_at,payload)
              select id,$2,$3,true,true,least(expires_at,statement_timestamp()+interval '1 hour'),$4::jsonb from management_grants
              where id=$1 and expires_at>clock_timestamp() returning id,expires_at`, grant, key, hash, snapshot).Scan(&receipt.ID, &receipt.Expires)
		} else {
			if err = tx.QueryRow(ctx, scopeReportQuery, grant, u.ID, in.From, in.To).Scan(&snapshot); err != nil {
				return err
			}
			// Export fields are explicitly listed so additions to the live dashboard remain private by default.
			err = tx.QueryRow(ctx, `with data as (select $5::jsonb as j)
			insert into management_exports(grant_id,request_key,request_hash,include_costs,expires_at,payload)
			select g.id,$2,$3,$4,least(g.expires_at,statement_timestamp()+interval '1 hour'),
			jsonb_build_object('version',1,'scope',jsonb_build_object('kind',$6::text,'name',$7::text),
			'from',j->'from','to',j->'to','generatedAt',j->'checkedAt',
			'outcomes',jsonb_build_object('runs',j->'runs','completed',j->'completed','waiting',j->'waiting','runStates',j->'runStates','checked',j->'checked',
			'reviewed',j->'reviewed','adopted',j->'adopted','editions',j->'editions'))
			|| case when $4 then jsonb_build_object('costs',jsonb_build_object('calls',j#>'{costs,calls}','tokens',j#>'{costs,tokens}',
			'recordedCny',j#>'{costs,recordedCny}','remainingEstimateCny',j#>'{costs,remainingEstimateCny}',
			'pending',j#>'{costs,pending}','unknown',j#>'{costs,unknown}')) else '{}'::jsonb end
			from management_grants g cross join data where g.id=$1 and g.expires_at>clock_timestamp() returning id,expires_at`,
				grant, key, hash, in.Costs, snapshot, kind, name).Scan(&receipt.ID, &receipt.Expires)
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "management.export.create", receipt.ID, map[string]any{"grant": grant, "request": in, "expiresAt": receipt.Expires})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, receipt)
}

func (s Server) recentExports(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	costLines := r.URL.Query().Get("kind") == "charges"
	var entries json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select (select coalesce(jsonb_agg(to_jsonb(x) order by x."createdAt" desc,x.id),'[]') from
		(select e.id,e.created_at as "createdAt",e.expires_at as "expiresAt",e.include_costs as costs,
			e.payload->'from' as "from",e.payload->'to' as "to" from management_exports e
			where e.grant_id=g.id and e.cost_lines=$3 and e.expires_at>statement_timestamp() and e.payload is not null
			and (not e.include_costs or g.include_costs) order by e.created_at desc,e.id limit 10) x)
		from management_grants g join members m on m.id=g.member_id and m.enabled
		where g.id=$1 and g.member_id=$2 and (case when $3 then g.financial_export else g.allow_export end) and g.revoked_at is null and g.expires_at>statement_timestamp()`,
		r.PathValue("id"), u.ID, costLines).Scan(&entries)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, entries)
}

func (s Server) downloadExport(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	grant, id := r.PathValue("id"), r.PathValue("export")
	var payload []byte
	var costLines bool
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var valid bool
		err := tx.QueryRow(ctx, `select e.payload,e.cost_lines,e.expires_at>clock_timestamp() and e.payload is not null
			from management_exports e join management_grants g on g.id=e.grant_id join members m on m.id=g.member_id and m.enabled
			where e.id=$1 and g.id=$2 and g.member_id=$3 and g.revoked_at is null and g.expires_at>clock_timestamp()
			and (case when e.cost_lines then g.financial_export else g.allow_export end) and (not e.include_costs or g.include_costs) for share of g,e,m`, id, grant, u.ID).Scan(&payload, &costLines, &valid)
		if err != nil {
			return err
		}
		if !valid {
			return web.Fail(410, "该文件已失效，请在当前授权下重新生成")
		}
		return database.Audit(ctx, tx, u.ID, "management.export.download", id, map[string]string{"grant": grant})
	})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="management-summary.json"`)
	if costLines {
		w.Header().Set("Content-Disposition", `attachment; filename="call-costs.json"`)
	}
	_, err = w.Write(payload)
	return err
}

func (s Server) exportPolicy(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Financial bool
		Enabled   *bool
		Expected  int
		Reason    string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Enabled == nil || in.Expected < 1 || in.Reason == "" || len(in.Reason) > 2000 {
		return web.Fail(422, "请核对当前授权版本并填写调整原因")
	}
	ctx := r.Context()
	grant := r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		query := `update management_grants set allow_export=$1,export_revision=export_revision+1
          where id=$2 and export_revision=$3 and allow_export<>$1 and revoked_at is null and expires_at>clock_timestamp()`
		if in.Financial {
			query = `update management_grants set financial_export=$1,export_revision=export_revision+1
              where id=$2 and export_revision=$3 and financial_export<>$1 and include_costs and revoked_at is null and expires_at>clock_timestamp()`
		}
		tag, err := tx.Exec(ctx, query, *in.Enabled, grant, in.Expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "授权已变化或已结束，请刷新后核对")
		}
		if !*in.Enabled {
			if _, err = tx.Exec(ctx, "update management_exports set payload=null where grant_id=$1 and cost_lines=$2 and payload is not null", grant, in.Financial); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "management.export.policy", grant, in)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
