package monitor

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a Alerts) Routes(m *http.ServeMux, auth identity.Server) {
	m.Handle("GET /api/admin/monitor/alerts", auth.Protect(true, a.list))
	m.Handle("POST /api/admin/monitor/alerts/rules", auth.Protect(true, a.save))
	m.Handle("PUT /api/admin/monitor/alerts/rules/{rule}", auth.Protect(true, a.save))
}

func (a Alerts) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var data json.RawMessage
	err := a.DB.QueryRow(r.Context(), `select jsonb_build_object('rules',coalesce((select jsonb_agg(jsonb_build_object(
	 'id',r.id,'name',r.name,'kind',r.kind,'teamId',r.team_id,'channel',r.channel,'signed',r.encrypted_signature is not null,
	 'threshold',r.threshold,'windowMinutes',r.window_minutes,'silenceMinutes',r.silence_minutes,'enabled',r.enabled,'revision',r.revision)
	 order by r.name,r.id) from alert_rules r),'[]'),'deliveries',coalesce((select jsonb_agg(jsonb_build_object(
	 'id',d.id,'ruleId',d.rule_id,'message',d.message,'state',d.state,'attempts',d.attempts,'createdAt',d.created_at,'deliveredAt',d.delivered_at)
	 order by d.created_at desc,d.id) from (select * from alert_deliveries order by created_at desc,id limit 100) d),'[]'))`).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

type alertInput struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	Channel          string  `json:"channel"`
	TeamID           *string `json:"teamId"`
	Threshold        int     `json:"threshold"`
	WindowMinutes    int     `json:"windowMinutes"`
	SilenceMinutes   int     `json:"silenceMinutes"`
	Webhook          *string `json:"webhook"`
	Signature        *string `json:"signature"`
	Enabled          bool    `json:"enabled"`
	ExpectedRevision int     `json:"expectedRevision"`
}

func (in *alertInput) valid() bool {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 120 || in.Threshold < 1 || in.Threshold > 10_000 ||
		in.WindowMinutes < 1 || in.WindowMinutes > 1440 || in.SilenceMinutes < 1 || in.SilenceMinutes > 10080 ||
		(in.Signature != nil && (len(*in.Signature) > 512 || strings.ContainsAny(*in.Signature, "\r\n\x00"))) {
		return false
	}
	if in.Channel != "feishu" && in.Channel != "dingtalk" {
		return false
	}
	if in.TeamID != nil {
		id, err := uuid.Parse(*in.TeamID)
		if err != nil || id == uuid.Nil {
			return false
		}
	}
	if in.Webhook != nil {
		if _, err := alertWebhook(in.Channel, *in.Webhook); err != nil {
			return false
		}
	}
	switch in.Kind {
	case "external_failure", "model_fallback":
		return true
	case "department_spike":
		return in.Threshold >= 100
	case "budget_limit":
		return in.Threshold == 80 || in.Threshold == 100
	default:
		return false
	}
}

func (a Alerts) save(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in alertInput
	if err := web.ReadLimited(w, r, &in, 12<<10); err != nil {
		return err
	}
	id := r.PathValue("rule")
	create := id == ""
	if !in.valid() || (create && in.Webhook == nil) || (!create && in.ExpectedRevision < 1) {
		return web.Fail(422, "请检查告警类型、阈值、静默时间和通知机器人配置")
	}
	if create {
		id = uuid.NewString()
	}
	var hook, sign []byte
	var err error
	if in.Webhook != nil {
		hook, err = a.Vault.Seal(*in.Webhook, alertScope(id, "webhook"))
		if err != nil {
			return err
		}
	}
	if in.Signature != nil && *in.Signature != "" {
		sign, err = a.Vault.Seal(*in.Signature, alertScope(id, "signature"))
		if err != nil {
			return err
		}
	}
	ctx := r.Context()
	var revision int
	err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		if create {
			err := tx.QueryRow(ctx, `insert into alert_rules(id,name,kind,team_id,threshold,window_minutes,silence_minutes,
			 channel,encrypted_webhook,encrypted_signature,enabled) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning revision`,
				id, in.Name, in.Kind, in.TeamID, in.Threshold, in.WindowMinutes, in.SilenceMinutes, in.Channel, hook, sign, in.Enabled).Scan(&revision)
			if err != nil {
				return err
			}
		} else {
			var current int
			var channel string
			if err := tx.QueryRow(ctx, `select revision,channel from alert_rules where id=$1 for update`, id).Scan(&current, &channel); err != nil {
				return err
			}
			if current != in.ExpectedRevision {
				return web.Fail(409, "告警规则已更新，请刷新后再保存")
			}
			if channel != in.Channel && hook == nil {
				return web.Fail(422, "切换通知渠道时需要填写新的机器人地址")
			}
			if err := tx.QueryRow(ctx, `update alert_rules set name=$2,kind=$3,team_id=$4,threshold=$5,window_minutes=$6,silence_minutes=$7,
			 channel=$8,encrypted_webhook=coalesce($9,encrypted_webhook),encrypted_signature=case when $10 then $11 else encrypted_signature end,
			 enabled=$12,revision=revision+1 where id=$1 returning revision`, id, in.Name, in.Kind, in.TeamID, in.Threshold,
				in.WindowMinutes, in.SilenceMinutes, in.Channel, hook, in.Signature != nil || in.Webhook != nil, sign, in.Enabled).Scan(&revision); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, actor.ID, "monitor.alert_rule.save", id,
			map[string]any{"kind": in.Kind, "channel": in.Channel, "enabled": in.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	return web.JSON(w, status, map[string]any{"id": id, "revision": revision})
}
