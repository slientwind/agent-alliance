package runtime

import (
	"context"

	"alliance/internal/projects"

	"github.com/jackc/pgx/v5"
)

func commitRuleBranchOutput(ctx context.Context, tx pgx.Tx, step Step, d contextData, text string, internal bool, outcome *Result) (bool, error) {
	var artifact *string
	var base *int
	var finished bool
	err := tx.QueryRow(ctx, `select artifact_id,base_version,finished from rule_branch_steps
        where (run_id,ordinal)=($1,$2) for update`, step.Run, step.Ordinal).Scan(&artifact, &base, &finished)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil || finished {
		return true, err
	}
	var edition *int
	if !internal {
		version := 1
		if artifact == nil {
			id, err := createRunArtifact(ctx, tx, step, d)
			if err != nil {
				return true, err
			}
			artifact = &id
		} else {
			var current int
			if err := tx.QueryRow(ctx, "select current_version from artifacts where id=$1 for update", *artifact).Scan(&current); err != nil {
				return true, err
			}
			if base == nil || *base != current {
				outcome.Wait = true
				return true, setState(ctx, tx, step, d.project, "blocked", "branch_artifact_changed", "重做期间候选版本已变化，请核对已有候选")
			}
			version = current + 1
		}
		if _, err := tx.Exec(ctx, "insert into artifact_versions(artifact_id,version,body,reason) values($1,$2,$3,'按当前有效依据重做')", *artifact, version, text); err != nil {
			return true, err
		}
		if err := storeRequirementEvidence(ctx, tx, step, d, *artifact, version); err != nil {
			return true, err
		}
		if _, err := tx.Exec(ctx, "select bind_artifact_remote_receipt($1,$2,$3,$4)", *artifact, version, step.Run, step.Ordinal); err != nil {
			return true, err
		}
		if _, err := tx.Exec(ctx, `insert into artifact_files select $1,$2,object_hash,filename,media_type
            from invocation_files where (run_id,ordinal)=($3,$4)`, *artifact, version, step.Run, step.Ordinal); err != nil {
			return true, err
		}
		if base != nil {
			if _, err := tx.Exec(ctx, "update artifacts set current_version=$2 where id=$1", *artifact, version); err != nil {
				return true, err
			}
			if _, err := tx.Exec(ctx, "select record_service_event($1,'artifact','ready',$2,$3,$4)", step.Run, step.Ordinal, *artifact, version); err != nil {
				return true, err
			}
		}
		edition = &version
	}
	if _, err := tx.Exec(ctx, `update rule_branch_steps set finished=true,artifact_id=$3,result_version=$4
        where (run_id,ordinal)=($1,$2)`, step.Run, step.Ordinal, artifact, edition); err != nil {
		return true, err
	}
	tag, err := tx.Exec(ctx, `update runs set status='completed' where id=$1
        and not exists(select from rule_branch_steps b where b.run_id=$1 and not b.finished)`, step.Run)
	if err != nil || tag.RowsAffected() == 0 {
		return true, err
	}
	if err := projects.Touch(ctx, tx, d.project, "review"); err != nil {
		return true, err
	}
	return true, projects.Note(ctx, tx, d.project, "assistant", "受影响的步骤已重做，请核对新版候选。沿用成果保留原版本。", nil)
}
