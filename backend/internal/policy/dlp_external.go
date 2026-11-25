package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxDLPExchangeTokens = 256
const maxDLPOutboundBytes = 512 << 10

var ErrDLPBlocked = resources.ErrExternalContentBlocked

var dlpPlaceholder = regexp.MustCompile(`\[[A-Z_]{1,32}_[0-9]{2,8}\]`)

type ExternalDLP struct {
	DB             *pgxpool.Pool
	Vault          resources.Vault
	Run            string
	Validation     string
	Actor          string
	Exchange       string
	Endpoint       string
	Classification Sensitivity
}

type dlpToken struct {
	Placeholder string  `json:"token"`
	Value       string  `json:"value"`
	Kind        DLPKind `json:"kind"`
}

func (p ExternalDLP) scopeID() string {
	if p.Run != "" {
		return p.Run
	}
	return p.Validation
}

func (p ExternalDLP) scope() string { return "dlp/" + p.scopeID() + "/" + p.Exchange }

func (p ExternalDLP) destination() (string, error) {
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Hostname() == "" || len(u.Hostname()) > 253 || len(p.Exchange) == 0 || len(p.Exchange) > 200 {
		return "", errors.New("invalid external content policy scope")
	}
	// Bind the encrypted dictionary to the same authority, including its port
	// when it differs from HTTPS's default.
	if port := u.Port(); port != "" && port != "443" {
		return strings.ToLower(u.Host), nil
	}
	return strings.ToLower(u.Hostname()), nil
}

func (p ExternalDLP) ProtectExternal(ctx context.Context, operation string, fields []string) ([]string, error) {
	destination, err := p.destination()
	if err != nil {
		return nil, err
	}
	var output []string
	var blocked bool
	err = database.WithTx(ctx, p.DB, func(tx pgx.Tx) error {
		allowed, err := p.authorized(ctx, tx, operation)
		if err != nil {
			return err
		}
		if !allowed {
			return web.Fail(409, "外发授权已变化")
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", p.scope()); err != nil {
			return err
		}
		scanner, err := loadDLPScanner(ctx, tx, p.Vault)
		if err != nil {
			return err
		}
		tokens, err := p.loadTokens(ctx, tx, destination)
		if err != nil {
			return err
		}
		initial := len(tokens)
		safe, updated, counts, denied := redactExternalFields(scanner, fields, p.Classification, tokens)
		output, tokens, blocked = safe, updated, denied
		outcome := "clear"
		if blocked {
			outcome = "blocked"
		} else if len(counts) > 0 {
			outcome = "masked"
			if len(tokens) > initial {
				encrypted, err := p.sealTokens(tokens)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `insert into dlp_egress_exchanges(run_id,validation_id,exchange,destination,encrypted_tokens,expires_at)
					values(nullif($1,'')::uuid,nullif($2,'')::uuid,$3,$4,$5,clock_timestamp()+interval '48 hours') on conflict(scope_id,exchange)
					do update set encrypted_tokens=excluded.encrypted_tokens,expires_at=excluded.expires_at`,
					p.Run, p.Validation, p.Exchange, destination, encrypted)
				if err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `insert into dlp_egress_audit_logs(run_id,validation_id,exchange,actor,destination,operation,outcome,counts)
			values(nullif($1,'')::uuid,nullif($2,'')::uuid,$3,$4,$5,$6,$7,$8)`, p.Run, p.Validation, p.Exchange, p.Actor, destination, operation, outcome, counts)
		return err
	})
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, ErrDLPBlocked
	}
	return output, nil
}

func (p ExternalDLP) authorized(ctx context.Context, tx pgx.Tx, operation string) (bool, error) {
	var allowed bool
	if p.Run != "" {
		err := tx.QueryRow(ctx, `select exists(select from runs r join project_members m on m.project_id=r.project_id
			where r.id=$1 and m.member_id=$2 and (r.status='running' or
			($3 like 'supplement/%' and r.status='reconcile')))`, p.Run, p.Actor, operation).Scan(&allowed)
		return allowed, err
	}
	err := tx.QueryRow(ctx, `select exists(select from validations v join members m on m.id=v.actor and m.enabled
		join agents a on a.id=v.agent_id and not a.disabled and not a.removed and not a.emergency
		join team_members tm on (tm.team_id,tm.member_id)=(a.team_id,m.id)
		join spend b on b.op_key='validation/'||v.id::text
		where v.id=$1 and m.id=$2 and (v.status='submitted' or (v.status='remote' and $3 like 'supplement/%'))
		and (b.project_id is null or exists(select from project_members pm where (pm.project_id,pm.member_id)=(b.project_id,m.id))))`,
		p.Validation, p.Actor, operation).Scan(&allowed)
	return allowed, err
}

func (p ExternalDLP) sealTokens(tokens []dlpToken) ([]byte, error) {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return nil, err
	}
	return p.Vault.Seal(string(raw), p.scope())
}

func (p ExternalDLP) loadTokens(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, destination string) ([]dlpToken, error) {
	var current string
	var cipher []byte
	var valid bool
	err := q.QueryRow(ctx, `select destination,encrypted_tokens,expires_at>clock_timestamp() from dlp_egress_exchanges
		where scope_id=$1 and exchange=$2`, p.scopeID(), p.Exchange).Scan(&current, &cipher, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current != destination {
		return nil, web.Fail(409, "外发接收方已变化")
	}
	if !valid {
		return nil, web.Fail(409, "这次外发的还原凭据已过期")
	}
	raw, err := p.Vault.Open(cipher, p.scope())
	if err != nil {
		return nil, err
	}
	var tokens []dlpToken
	err = json.Unmarshal([]byte(raw), &tokens)
	return tokens, err
}

func (p ExternalDLP) RestoreExternal(ctx context.Context, value string) (string, error) {
	if !strings.Contains(value, "[") {
		return value, nil
	}
	destination, err := p.destination()
	if err != nil {
		return "", err
	}
	tokens, err := p.loadTokens(ctx, p.DB, destination)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 {
		return value, nil
	}
	pairs := make([]string, 0, len(tokens)*2)
	for _, token := range tokens {
		pairs = append(pairs, token.Placeholder, token.Value)
	}
	return strings.NewReplacer(pairs...).Replace(value), nil
}

func redactExternalFields(scanner DLPScanner, fields []string, label Sensitivity, tokens []dlpToken) ([]string, []dlpToken, map[DLPKind]int, bool) {
	counts := make(map[DLPKind]int)
	out := make([]string, 0, len(fields))
	byValue := make(map[string]string, len(tokens))
	used := make(map[string]bool, len(tokens))
	issued := make(map[string]bool, len(tokens))
	for _, item := range tokens {
		byValue[tokenKey(item.Kind, item.Value)] = item.Placeholder
		used[item.Placeholder] = true
		issued[item.Placeholder] = true
	}
	total := 0
	for _, value := range fields {
		total += len(value)
		if total > maxDLPOutboundBytes {
			counts[DLPLabel]++
			return nil, tokens, counts, true
		}
		for _, marker := range dlpPlaceholder.FindAllString(value, -1) {
			if issued[marker] {
				counts[DLPLabel]++
				return nil, tokens, counts, true
			}
			used[marker] = true
		}
	}
	for _, value := range fields {
		if web.TraceText(value) != value {
			counts[DLPLabel]++
			return nil, tokens, counts, true
		}
		matches := scanner.Scan("", value, label)
		if len(matches) == 0 {
			out = append(out, value)
			continue
		}
		var safe strings.Builder
		start := 0
		for _, match := range matches {
			counts[match.Kind]++
			if match.Sensitivity == SensitivitySecret {
				return nil, tokens, counts, true
			}
			original := value[match.Start:match.End]
			key := tokenKey(match.Kind, original)
			marker := byValue[key]
			if marker == "" {
				if len(tokens) >= maxDLPExchangeTokens {
					return nil, tokens, counts, true
				}
				marker = newPlaceholder(match.Kind, used)
				byValue[key] = marker
				used[marker] = true
				tokens = append(tokens, dlpToken{Placeholder: marker, Value: original, Kind: match.Kind})
			}
			safe.WriteString(value[start:match.Start])
			safe.WriteString(marker)
			start = match.End
		}
		safe.WriteString(value[start:])
		out = append(out, safe.String())
	}
	return out, tokens, counts, false
}

func tokenKey(kind DLPKind, value string) string { return string(kind) + "\x00" + value }

func newPlaceholder(kind DLPKind, used map[string]bool) string {
	for i := 1; ; i++ {
		value := fmt.Sprintf("[%s_%02d]", strings.ToUpper(string(kind)), i)
		if !used[value] {
			return value
		}
	}
}

func PruneDLPExchanges(ctx context.Context, db *pgxpool.Pool) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_, err := db.Exec(ctx, "delete from dlp_egress_exchanges where expires_at<=clock_timestamp()")
		if err != nil && ctx.Err() == nil {
			slog.Warn("expired DLP exchange pruning deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
