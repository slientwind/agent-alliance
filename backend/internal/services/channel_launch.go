package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChannelConversation identifies one conversation under one binding generation.
type ChannelConversation struct {
	Binding, Chat, Thread string
}

type ChannelLaunchReceipt struct {
	ID      string `json:"id"`
	Project string `json:"project"`
}

type channelLaunch struct {
	ChannelConversation
	id, service, run, provider string
	version, routing, calls    int
	sealed                     []byte
	expires                    time.Time
}

type ChannelLaunchPreview struct {
	Service, Name, Team, Reviewer, Goal, WebOnlyReason string
	Version, Calls                                     int
	Delivery                                           ServiceDeliveryContract
	Receipt                                            *ChannelLaunchReceipt
}

func channelLaunchScope(id string) string { return "im-service-launch/" + id }

func channelBinding(ctx context.Context, tx pgx.Tx, actor, binding string) (connection string, err error) {
	err = tx.QueryRow(ctx, `select b.connection from im_bindings b join members m on m.id=b.member_id
 where b.id=$1 and m.id=$2 and m.enabled and m.kind='person' and m.departed_at is null for share of b,m`, binding, actor).Scan(&connection)
	return connection, err
}

func PrepareChannelLaunch(ctx context.Context, tx pgx.Tx, vault resources.Vault, scope ChannelConversation, actor, service, goal string) (string, error) {
	if web.Blank(goal) || utf8.RuneCountInString(goal) > 1000 {
		return "", web.Fail(422, "请提供不超过 1000 字的工作目标")
	}
	if _, err := channelBinding(ctx, tx, actor, scope.Binding); err != nil {
		return "", err
	}
	// Lock or remove the prior preview before reading the service publication.
	// Confirmations take locks in the same order.
	if err := DiscardChannelLaunch(ctx, tx, scope); err != nil {
		return "", err
	}
	info, err := readServiceAdmission(ctx, tx, subject{Actor: actor, Service: service}, 0)
	if err != nil {
		return "", err
	}
	if info.count == 0 {
		return "", web.Fail(409, "该服务还没有可运行的步骤")
	}
	id := uuid.NewString()
	sealed, err := vault.Seal(goal, channelLaunchScope(id))
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `insert into im_service_launches
 (id,binding_id,chat_id,thread_id,service_id,version,routing_revision,calls,goal)
 values($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, scope.Binding, scope.Chat, scope.Thread, service, info.version, info.routingRevision, info.count, sealed)
	return id, err
}

func DiscardChannelLaunch(ctx context.Context, tx pgx.Tx, scope ChannelConversation) error {
	_, err := tx.Exec(ctx, `delete from im_service_launches where (binding_id,chat_id,thread_id)=($1,$2,$3) and run_id is null`,
		scope.Binding, scope.Chat, scope.Thread)
	return err
}

func readChannelLaunch(ctx context.Context, tx pgx.Tx, actor, id, service string, scope ChannelConversation) (channelLaunch, error) {
	var p channelLaunch
	// Resolve the binding before locking the preview. Unlinking locks the
	// binding first and cascades to its previews.
	err := tx.QueryRow(ctx, `select p.binding_id from im_service_launches p join im_bindings b on b.id=p.binding_id
 where p.id=$1 and b.member_id=$2 and (nullif($3,'') is null or p.service_id=nullif($3,'')::uuid)
 and (nullif($4,'') is null or (p.binding_id,p.chat_id,p.thread_id)=(nullif($4,'')::uuid,$5,$6))`,
		id, actor, service, scope.Binding, scope.Chat, scope.Thread).Scan(&p.Binding)
	if err != nil {
		return p, err
	}
	connection, err := channelBinding(ctx, tx, actor, p.Binding)
	if err != nil {
		return p, err
	}
	p.id = id
	p.provider, _, _ = strings.Cut(connection, "/")
	var expired bool
	err = tx.QueryRow(ctx, `select service_id,chat_id,thread_id,version,routing_revision,calls,goal,
 coalesce(run_id::text,''),expires_at,expires_at<=clock_timestamp()
 from im_service_launches where (id,binding_id)=($1,$2) for update`, id, p.Binding).
		Scan(&p.service, &p.Chat, &p.Thread, &p.version, &p.routing, &p.calls, &p.sealed, &p.run, &p.expires, &expired)
	if err == nil && p.run == "" && expired {
		err = web.Fail(410, "服务预览已过期，请回到原单聊重新发起")
	}
	return p, err
}

func (p channelLaunch) receipt(ctx context.Context, tx pgx.Tx, actor string) (*ChannelLaunchReceipt, error) {
	if p.run == "" {
		return nil, nil
	}
	r := ChannelLaunchReceipt{ID: p.run}
	err := tx.QueryRow(ctx, `select r.project_id from runs r join services s on s.id=r.service_id
 join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,$2)
 join team_members tm on (tm.team_id,tm.member_id)=(s.team_id,$2)
 where r.id=$1 and r.actor=$2 and (r.service_id,r.service_version)=($3,$4)`, p.run, actor, p.service, p.version).Scan(&r.Project)
	return &r, err
}

func (p channelLaunch) admission(ctx context.Context, tx pgx.Tx, actor string) (serviceAdmission, error) {
	info, err := readServiceAdmission(ctx, tx, subject{Actor: actor, Service: p.service}, p.version)
	if err == nil && (info.routingRevision != p.routing || info.count != p.calls) {
		err = web.Fail(409, "服务发布状态已变化，请回到原单聊核对后重新发起")
	}
	return info, err
}

func (p channelLaunch) externalAgents(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `select distinct a.name from (`+serviceDependencies+`) pins
 join agent_revisions a on (a.agent_id,a.revision)=(pins.agent_id,pins.agent_revision)
 where pins.version=$2 and a.card is not null order by a.name`, p.service, p.version)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func channelWebOnly(info serviceAdmission, external []string) string {
	if len(external) > 0 {
		return "此服务含第三方 Agent。请在网页核对数据接收方后确认。"
	}
	if info.usesPictures || info.needsSource || info.csvChoice {
		return "此服务需要资料。请在网页附上原件后确认。"
	}
	return ""
}

func ReadChannelPreview(ctx context.Context, tx pgx.Tx, vault resources.Vault, scope ChannelConversation, actor, id string) (ChannelLaunchPreview, error) {
	p, err := readChannelLaunch(ctx, tx, actor, id, "", scope)
	if err != nil {
		return ChannelLaunchPreview{}, err
	}
	view := ChannelLaunchPreview{Service: p.service, Version: p.version, Calls: p.calls}
	view.Receipt, err = p.receipt(ctx, tx, actor)
	if err != nil || view.Receipt != nil {
		return view, err
	}
	info, err := p.admission(ctx, tx, actor)
	if err != nil {
		return view, err
	}
	external, err := p.externalAgents(ctx, tx)
	if err != nil {
		return view, err
	}
	view.Name, view.Team, view.Reviewer = info.name, info.teamName, info.reviewerName
	view.WebOnlyReason = channelWebOnly(info, external)
	if err := tx.QueryRow(ctx, "select service_delivery_contract($1,$2)", p.service, p.version).Scan(&view.Delivery); err != nil {
		return view, err
	}
	view.Goal, err = vault.Open(p.sealed, channelLaunchScope(p.id))
	return view, err
}

func ConfirmChannelLaunch(ctx context.Context, tx pgx.Tx, vault resources.Vault, scope ChannelConversation, actor, id string) (*ChannelLaunchReceipt, error) {
	p, err := readChannelLaunch(ctx, tx, actor, id, "", scope)
	if err != nil {
		return nil, err
	}
	if receipt, err := p.receipt(ctx, tx, actor); receipt != nil || err != nil {
		return receipt, err
	}
	info, err := p.admission(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	external, err := p.externalAgents(ctx, tx)
	if err != nil {
		return nil, err
	}
	if reason := channelWebOnly(info, external); reason != "" {
		return nil, web.Fail(422, reason)
	}
	return p.accept(ctx, tx, vault, actor, request{
		AwaitLookupReceipt: info.lookup, AwaitCalculationReceipt: info.calculation,
	})
}

func (p channelLaunch) accept(ctx context.Context, tx pgx.Tx, vault resources.Vault, actor string, in request) (*ChannelLaunchReceipt, error) {
	var err error
	in.Input, err = vault.Open(p.sealed, channelLaunchScope(p.id))
	if err != nil {
		return nil, err
	}
	in.Version, in.AuthorizeCalls = p.version, p.calls
	// The locked preview is the public idempotency boundary. Use a separate
	// server-generated run key that other entry points cannot claim.
	result, err := launch(ctx, tx, subject{Actor: actor, Service: p.service}, in, channelLaunchScope(web.Token()))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `update im_service_launches set goal=null,run_id=$2,accepted_at=clock_timestamp()
 where id=$1`, p.id, result["id"]); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `insert into im_threads(binding_id,chat_id,thread_id,project_id) values($1,$2,$3,$4)
 on conflict(binding_id,chat_id,thread_id) do update set project_id=excluded.project_id`, p.Binding, p.Chat, p.Thread, result["project"]); err != nil {
		return nil, err
	}
	return &ChannelLaunchReceipt{ID: result["id"], Project: result["project"]}, nil
}

type channelLaunchView struct {
	Provider       string                `json:"provider"`
	Input          string                `json:"input,omitempty"`
	ExpiresAt      time.Time             `json:"expiresAt"`
	Entry          json.RawMessage       `json:"entry,omitempty"`
	ExternalAgents []string              `json:"externalAgents,omitempty"`
	Receipt        *ChannelLaunchReceipt `json:"receipt,omitempty"`
}

func (s Server) channelLaunchEntry(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var view channelLaunchView
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := readChannelLaunch(ctx, tx, u.ID, r.PathValue("launch"), r.PathValue("id"), ChannelConversation{})
		if err != nil {
			return err
		}
		view.Provider, view.ExpiresAt = p.provider, p.expires
		view.Receipt, err = p.receipt(ctx, tx, u.ID)
		if err != nil || view.Receipt != nil {
			return err
		}
		if _, err = p.admission(ctx, tx, u.ID); err != nil {
			return err
		}
		view.Input, err = s.Planner.Catalog.Vault.Open(p.sealed, channelLaunchScope(p.id))
		if err != nil {
			return err
		}
		view.ExternalAgents, err = p.externalAgents(ctx, tx)
		if err != nil {
			return err
		}
		view.Entry, err = readEntry(ctx, tx, p.service, u.ID)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, view)
}

type channelConfirmation struct {
	Acknowledged            bool
	Attachment              *Attachment
	Attachments             []Attachment
	AwaitLookupReceipt      bool
	AwaitCalculationReceipt bool
	AllowPartialDelivery    bool
}

func (s Server) confirmChannelLaunch(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var attempt channelConfirmation
	if err := web.ReadLimited(w, r, &attempt, serviceRequestLimit); err != nil {
		return err
	}
	if !attempt.Acknowledged {
		return web.Fail(422, "请先确认工作目标和调用授权")
	}
	in := request{Attachment: attempt.Attachment, Attachments: attempt.Attachments,
		AwaitLookupReceipt: attempt.AwaitLookupReceipt, AwaitCalculationReceipt: attempt.AwaitCalculationReceipt,
		AllowPartialDelivery: attempt.AllowPartialDelivery}
	if err := in.validateFiles(); err != nil {
		return err
	}
	var receipt *ChannelLaunchReceipt
	confirm := func(tx pgx.Tx, preflight bool) error {
		p, err := readChannelLaunch(ctx, tx, u.ID, r.PathValue("launch"), r.PathValue("id"), ChannelConversation{})
		if err != nil {
			return err
		}
		receipt, err = p.receipt(ctx, tx, u.ID)
		if err != nil || receipt != nil {
			return err
		}
		info, err := p.admission(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if preflight {
			in.AuthorizeCalls = p.calls
			return info.validateInput(in)
		}
		receipt, err = p.accept(ctx, tx, s.Planner.Catalog.Vault, u.ID, in)
		return err
	}
	if in.hasDocuments() || in.pictureCount() > 0 {
		// Readable originals are prepared outside the locking transaction.
		if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return confirm(tx, true) }); err != nil {
			return err
		}
		if receipt == nil {
			if err := in.prepareOriginals(ctx); err != nil {
				return err
			}
		}
	}
	if receipt == nil {
		if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error { return confirm(tx, false) }); err != nil {
			return err
		}
	}
	if receipt == nil {
		return errors.New("missing channel launch receipt")
	}
	return web.JSON(w, 202, receipt)
}
