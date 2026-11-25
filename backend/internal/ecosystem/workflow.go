package ecosystem

import (
	"context"
	"errors"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const ScanWorkflowName = "EcosystemScanWorkflow"
const ScanActivityName = "Ecosystem.Scan"

type ScanRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

type scanOccurrence struct {
	Source ScanRef
	Key    string
}

type Activities struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
	HTTP  *http.Client
}

func EcosystemScanWorkflow(ctx workflow.Context, ref ScanRef) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 115 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	input := scanOccurrence{Source: ref, Key: workflow.GetInfo(ctx).WorkflowExecution.ID}
	return workflow.ExecuteActivity(ctx, ScanActivityName, input).Get(ctx, nil)
}

func (a Activities) Scan(ctx context.Context, occurrence scanOccurrence) error {
	src, scan, err := a.claimScan(ctx, occurrence)
	if err != nil || scan == "" {
		return err
	}
	f := fetcher{Egress: egress.Policy{DB: a.DB}, HTTP: a.HTTP, Vault: a.Vault, Current: func(ctx context.Context) bool {
		var current bool
		err := a.DB.QueryRow(ctx, `select exists(select from ecosystem_sources where id=$1 and revision=$2 and enabled)`,
			src.ID, src.Revision).Scan(&current)
		return err == nil && current
	}}
	batch, err := f.discovery(ctx, src)
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err == nil {
		err = a.saveScan(done, src, scan, batch)
	}
	if err != nil {
		if _, writeErr := a.DB.Exec(done, `update ecosystem_scans set status='failed',detail='上游目录暂时无法读取',
		 finished_at=clock_timestamp() where id=$1 and status='running'`, scan); writeErr != nil {
			return writeErr
		}
		return errDiscovery
	}
	return nil
}

func (a Activities) claimScan(ctx context.Context, occurrence scanOccurrence) (source, string, error) {
	var src source
	var scan string
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `select id,kind,endpoint,revision,source_config,credential,catalog_cursor,enabled
		 from ecosystem_sources where id=$1 for update`, occurrence.Source.ID).
			Scan(&src.ID, &src.Kind, &src.Endpoint, &src.Revision, &src.Config, &src.Credential, &src.Cursor, &active)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (!active || src.Revision != occurrence.Source.Revision) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ecosystem_scans set status='failed',detail='扫描未完成',finished_at=clock_timestamp()
		 where source_id=$1 and status='running' and started_at<clock_timestamp()-interval '3 minutes'`, src.ID); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into ecosystem_scans(source_id,source_revision,workflow_key)
		 values($1,$2,$3) on conflict do nothing returning id`, src.ID, src.Revision, occurrence.Key).Scan(&scan)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return src, scan, err
}

func (a Activities) saveScan(ctx context.Context, src source, scan string, batch feedBatch) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var revision int
		var enabled bool
		if err := tx.QueryRow(ctx, `select revision,enabled from ecosystem_sources where id=$1 for update`, src.ID).
			Scan(&revision, &enabled); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `select status='running' from ecosystem_scans where id=$1 for update`, scan).Scan(&active); err != nil {
			return err
		}
		if !active {
			return nil
		}
		if revision != src.Revision || !enabled {
			_, err := tx.Exec(ctx, `update ecosystem_scans set status='skipped',detail='扫描设置已变化',finished_at=clock_timestamp()
			 where id=$1`, scan)
			return err
		}
		added, err := saveCandidates(ctx, tx, src, batch.Items)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ecosystem_sources set catalog_cursor=$2 where id=$1`, src.ID, batch.Cursor); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update ecosystem_scans set status='completed',found=$2,added=$3,finished_at=clock_timestamp()
		 where id=$1`, scan, len(batch.Items), added)
		return err
	})
}

func saveCandidates(ctx context.Context, tx pgx.Tx, src source, items []candidate) (int, error) {
	batch := &pgx.Batch{}
	for _, item := range items {
		batch.Queue(`with inserted as (
		 insert into ecosystem_catalog(source_id,source_revision,upstream_key,upstream_version,upstream_digest,
		  kind,name,description,homepage,package_url,metadata,upstream_updated_at)
		 values($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),nullif($10,''),$11,$12)
		 on conflict(source_id,upstream_key,upstream_version,upstream_digest) do nothing returning id
		), refreshed as (
		 update ecosystem_catalog set last_seen_at=clock_timestamp()
		 where (source_id,upstream_key,upstream_version,upstream_digest)=($1,$3,$4,$5)
		  and not exists(select from inserted)
		) select exists(select from inserted)`,
			src.ID, src.Revision, item.Key, item.Version, item.Digest, item.Kind, item.Name, item.Description,
			item.Homepage, item.PackageURL, item.Metadata, item.Updated)
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	added := 0
	for range items {
		var inserted bool
		if err := results.QueryRow().Scan(&inserted); err != nil {
			return 0, err
		}
		if inserted {
			added++
		}
	}
	return added, results.Close()
}
