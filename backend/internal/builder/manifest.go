package builder

import (
	"alliance/internal/web"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type Manifest struct {
	Digest     string          `json:"digest"`
	Definition json.RawMessage `json:"definition"`
}

type Evidence struct {
	Passed   bool            `json:"passed"`
	Digest   string          `json:"digest"`
	Snapshot json.RawMessage `json:"snapshot"`
}

const DriftMessage = "Agent 定义与发布清单不一致，请由维护人核对修订"

// Selection is the build and evidence the reviewer actually saw.
type Selection struct {
	BuildDigest    string `json:"buildDigest"`
	EvidenceDigest string `json:"evidenceDigest"`
}

func (s Selection) Match(build Manifest, evidence Evidence) bool {
	return (s.BuildDigest == "" && s.EvidenceDigest == "") ||
		(s.BuildDigest == build.Digest && s.EvidenceDigest == evidence.Digest)
}

type Queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Read(ctx context.Context, db Queryer, agent string, revision int) (Manifest, error) {
	var build Manifest
	err := db.QueryRow(ctx, `select agent_build_digest(definition),definition
        from (select agent_build_definition($1,$2) as definition) b where definition is not null`,
		agent, revision).Scan(&build.Digest, &build.Definition)
	return build, err
}

// Capture stores the private-data-free definition in the same transaction as
// the trial, review request, release receipt or runtime use that references it.
func Capture(ctx context.Context, tx pgx.Tx, agent string, revision int) (Manifest, error) {
	build, err := Read(ctx, tx, agent, revision)
	if err != nil {
		return build, err
	}
	_, err = tx.Exec(ctx, `insert into agent_build_manifests(agent_id,revision,digest,definition)
        values($1,$2,$3,$4) on conflict do nothing`, agent, revision, build.Digest, build.Definition)
	return build, err
}

// EvidenceFor retains the existing acceptance semantics while pinning the
// effective trial for each check. Pre-upgrade trials have an explicit legacy flag.
func EvidenceFor(ctx context.Context, q Queryer, agent string, revision int, digest string) (Evidence, error) {
	var e Evidence
	err := q.QueryRow(ctx, `with checks as materialized (
          select c.id,c.ordinal,c.evidence_id,c.passed and (v.build_digest is null or v.build_digest=$3) as passed,
            v.id is not null and v.build_digest is null as legacy
          from check_outcomes c left join validations v on v.id=c.evidence_id
          where (c.agent_id,c.revision)=($1,$2)
        ), standalone as (
          select v.id,v.build_digest is null as legacy from validations v
          where (v.agent_id,v.revision)=($1,$2) and v.status='passed'
            and (v.build_digest is null or v.build_digest=$3)
            and not exists(select from validation_withdrawals w where w.trial_id=v.id)
          order by v.created_at desc,v.id desc limit 1
        ), proof as (
          select case when exists(select from checks) then jsonb_build_object(
            'mode','required-checks','passed',not exists(select from checks where passed is not true),
            'items',array(select jsonb_build_object('check',id,'trial',evidence_id,'passed',coalesce(passed,false),'legacy',legacy)
                         from checks order by ordinal))
          else jsonb_build_object('mode','standalone','passed',exists(select from standalone),
            'items',array(select jsonb_build_object('trial',id,'passed',true,'legacy',legacy) from standalone))
          end as snapshot
        ) select (snapshot->>'passed')::boolean,agent_build_digest(snapshot),snapshot from proof`,
		agent, revision, digest).Scan(&e.Passed, &e.Digest, &e.Snapshot)
	return e, err
}

// CheckPublished uses the receipt for this revision, even when the catalog
// has subsequently moved to another revision.
func CheckPublished(ctx context.Context, q Queryer, agent string, revision int) error {
	var matches bool
	if err := q.QueryRow(ctx, `select agent_build_matches_publication($1,$2)`,
		agent, revision).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return web.Fail(409, DriftMessage)
	}
	return nil
}

func TrialMatches(ctx context.Context, tx pgx.Tx, trial string) (bool, error) {
	var matches bool
	err := tx.QueryRow(ctx, `select v.build_digest is null or v.build_digest=agent_build_digest(agent_build_definition(v.agent_id,v.revision))
          from validations v where v.id=$1`, trial).Scan(&matches)
	return matches, err
}
