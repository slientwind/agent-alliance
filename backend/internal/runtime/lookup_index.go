package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const indexWaitReason = "资料仍在建立语义索引。已保存选词，索引就绪后将继续核对。"

type indexRecheck struct {
	Plan, Control int
	Goal          string
}

type indexCitation struct {
	Source              string
	Version, Start, End int
	Hash                string
}

// The wait record stores positions and fingerprints. The authorized source
// remains the only copy from which an answered step can read the quote.
func reopenIndexCitations(observation string, files []knowledge.InputSource) ([]lookupHit, error) {
	var saved []indexCitation
	if err := json.Unmarshal([]byte(observation), &saved); err != nil {
		return nil, err
	}
	var hits []lookupHit
	for _, c := range saved {
		for i, file := range files {
			if file.ID != c.Source || file.Version != c.Version || file.ReceiptStep != 0 {
				continue
			}
			chars := []rune(file.Text)
			if c.Start >= 0 && c.End <= len(chars) && c.End > c.Start && c.End-c.Start <= 320 {
				quote := string(chars[c.Start:c.End])
				if receiptDigest([]byte(quote)) == c.Hash {
					hits = append(hits, lookupHit{source: i, version: file.Version, start: c.Start, end: c.End, text: quote})
				}
			}
			break
		}
	}
	return hits, nil
}

func parkLookupIndex(ctx context.Context, tx pgx.Tx, step Step, d contextData, announce bool) error {
	note := ""
	if announce {
		note = indexWaitReason
	}
	if err := setState(ctx, tx, step, d.project, "blocked", "lookup_index_waiting", note); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into lookup_index_waits(run_id,ordinal,plan_version,active,deadline,next_check_at)
 values($1,$2,$3,true,clock_timestamp()+interval '2 minutes',clock_timestamp()+interval '3 seconds')
 on conflict(run_id,ordinal) do update set active=true,observation=null,plan_version=excluded.plan_version,
 deadline=case when lookup_index_waits.plan_version=excluded.plan_version then lookup_index_waits.deadline else excluded.deadline end,
 next_check_at=excluded.next_check_at`, step.Run, step.Ordinal, d.planVersion); err != nil {
		return err
	}
	if d.awaitLookupReceipt {
		if _, err := tx.Exec(ctx, `insert into calculation_requests(run_id,ordinal,reason,kind) values($1,$2,$3,'lookup')
 on conflict(run_id,ordinal) do update set reason=excluded.reason`, step.Run, step.Ordinal,
			"资料仍在建立语义索引。若已掌握其他依据，可以先补充原句。"); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "select record_service_event($1,'step','lookup_index_waiting',$2)", step.Run, step.Ordinal)
	return err
}

// Release only the exact index wait that owns the current block. Committing the
// state change with the existing wake record lets replacement workers deliver it.
func (a Activities) wakeIndexedLookup(ctx context.Context) error {
	if !knowledge.SemanticLookupEnabled() {
		return nil
	}
	return database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var step Step
		var project string
		err := tx.QueryRow(ctx, `select w.run_id,w.ordinal,r.project_id from lookup_index_waits w join runs r on r.id=w.run_id
 where w.active and r.status='blocked' and r.plan_version=w.plan_version
 and not exists(select from calculation_requests cr where cr.run_id=r.id and cr.ordinal<>w.ordinal)
 and w.next_check_at<=clock_timestamp() and w.deadline>clock_timestamp()
 order by w.next_check_at,w.run_id limit 1 for update of r skip locked`).Scan(&step.Run, &step.Ordinal, &project)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		pending, err := knowledge.SourceIndexPending(ctx, tx, step.Run)
		if err != nil {
			return err
		}
		if pending {
			_, err := tx.Exec(ctx, "update lookup_index_waits set next_check_at=clock_timestamp()+interval '3 seconds' where run_id=$1 and ordinal=$2", step.Run, step.Ordinal)
			return err
		}
		if err := projects.ClearLookupIndexGate(ctx, tx, step.Run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update runs set status='running',control_revision=control_revision+1 where id=$1", step.Run); err != nil {
			return err
		}
		if err := projects.TouchRun(ctx, tx, step.Run, project, "running"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into receipt_wakes values($1) on conflict do nothing", step.Run)
		return err
	})
}

// Recheck only the local search. The paid first step and both its receipt and
// artifact stay intact. The normal answer step reopens the exact quotes before
// accepting the second paid intent.
func (a Activities) recheckLookupIndex(ctx context.Context, step Step, d contextData, recheck indexRecheck) (Result, error) {
	db := a.Catalog.DB
	match, probeErr := knowledge.PinnedSemanticPassages(ctx, db, step.Run, recheck.Goal)
	hits, _ := semanticLookupHits(match.Hits, d.lookupFiles)
	result := Result{Wait: true}
	var ready bool
	err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var current indexRecheck
		var state string
		if err := tx.QueryRow(ctx, "select status,plan_version,control_revision from runs where id=$1 for update", step.Run).Scan(&state, &current.Plan, &current.Control); err != nil {
			return err
		}
		if state != "running" || current.Plan != recheck.Plan || current.Control != recheck.Control {
			result.Stop = runEnded(state)
			result.Wait = !result.Stop
			return nil
		}
		var granted bool
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, step.Run).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			return setState(ctx, tx, step, d.project, "blocked", "delegation_expired", "继续检索前应用或成员委托已失效")
		}
		if _, _, err := knowledge.Inputs(ctx, tx, step.Run); err != nil {
			var problem web.Problem
			if !errors.As(err, &problem) {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "input_changed", problem.Message)
		}
		if probeErr != nil || !knowledge.SemanticLookupEnabled() || (match.IndexPending && len(hits) == 0) {
			return parkLookupIndex(ctx, tx, step, d, false)
		}
		saved := make([]indexCitation, 0, len(hits))
		for _, hit := range hits {
			saved = append(saved, indexCitation{d.lookupFiles[hit.source].ID, hit.version, hit.start, hit.end, receiptDigest([]byte(hit.text))})
		}
		observation, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		outcome := "lookup_index_no_evidence"
		if len(hits) > 0 {
			outcome = "lookup_index_rechecked"
		}
		tag, err := tx.Exec(ctx, "update lookup_index_waits set observation=$3,active=false where run_id=$1 and ordinal=$2 and plan_version=$4 and observation is null", step.Run, step.Ordinal, string(observation), recheck.Plan)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			ready = true
			return nil
		}
		if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint',$3)", step.Run, step.Ordinal, outcome); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select record_service_event($1,'step',$3,$2)", step.Run, step.Ordinal, outcome); err != nil {
			return err
		}
		ready = true
		return nil
	})
	if err != nil || !ready {
		return result, err
	}
	return a.ExecuteStep(ctx, step)
}
