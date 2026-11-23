package knowledge

import (
	"context"
	"slices"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// ColumnIssue describes choices reported by the pinned calculation operation.
type ColumnIssue struct {
	Source  string            `json:"source"`
	Version int               `json:"version"`
	Name    string            `json:"name"`
	Missing []string          `json:"missing"`
	Choices []string          `json:"choices"`
	Roles   map[string]string `json:"roles,omitempty"`
}

// Serialize copying header text with source withdrawal and final content cleanup.
// Project writers and source lifecycle operations take the project lock first.
func LockCorrectionSource(ctx context.Context, tx pgx.Tx, project, source string) (bool, error) {
	if _, err := tx.Exec(ctx, `select id from projects where id=$1 for update`, project); err != nil {
		return false, err
	}
	var readable bool
	err := tx.QueryRow(ctx, `select revoked_at is null and purged_at is null from sources where id=$1 and project_id=$2 for share`, source, project).Scan(&readable)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return readable, err
}

// ColumnCorrection binds a human interpretation to the original file revision.
type ColumnCorrection struct {
	Source  string            `json:"source"`
	Version int               `json:"version"`
	Columns map[string]string `json:"columns"`
}

// ColumnDocument preserves the object shape of single-file receipts and
// decisions, including records written before batch correction was available.
func ColumnDocument[T any](items []T) any {
	switch len(items) {
	case 0:
		return nil
	case 1:
		return items[0]
	default:
		return items
	}
}

func ReadableColumnIssues(ctx context.Context, tx pgx.Tx, project string, files []InputSource, issues []ColumnIssue) ([]ColumnIssue, error) {
	seen := make(map[string]bool, len(issues))
	for i, issue := range issues {
		pin := slices.IndexFunc(files, func(file InputSource) bool {
			return file.ID == issue.Source && file.Version == issue.Version
		})
		if pin == -1 || seen[issue.Source] || len(issue.Missing) == 0 || len(issue.Choices) < len(issue.Missing) {
			return nil, nil
		}
		seen[issue.Source] = true
		readable, err := LockCorrectionSource(ctx, tx, project, issue.Source)
		if err != nil || !readable {
			return nil, err
		}
		issues[i].Name = files[pin].Name
	}
	return issues, nil
}

func ValidateColumnCorrections(ctx context.Context, tx pgx.Tx, project, run string, ordinal int, corrections []ColumnCorrection) error {
	rows, err := tx.Query(ctx, `select item from invocations i cross join lateral column_correction_items(i.input_issue) as item
 where i.run_id=$1 and i.ordinal=$2`, run, ordinal)
	if err != nil {
		return err
	}
	issues, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ColumnIssue, error) {
		var issue ColumnIssue
		err := row.Scan(&issue)
		return issue, err
	})
	if err != nil {
		return err
	}
	if len(issues) == 0 || len(corrections) != len(issues) {
		return web.Fail(409, "请一次核对本次所有待补正的资料和字段")
	}
	for _, issue := range issues {
		at := slices.IndexFunc(corrections, func(c ColumnCorrection) bool {
			return c.Source == issue.Source && c.Version == issue.Version
		})
		if at == -1 || len(corrections[at].Columns) != len(issue.Missing) {
			return web.Fail(409, "请核对原资料版本和本次缺少的字段")
		}
		c := corrections[at]
		used := map[string]bool{}
		for _, field := range issue.Missing {
			original := c.Columns[field]
			if !slices.Contains(issue.Choices, original) || used[original] {
				return web.Fail(422, "请为同一份资料中缺少的字段选用不同的原表列")
			}
			used[original] = true
		}
	}
	// Lock all affected sources before reading the pinned run input list. The
	// whole authorization must fail if any file is inaccessible.
	for _, issue := range issues {
		readable, err := LockCorrectionSource(ctx, tx, project, issue.Source)
		if err != nil {
			return err
		}
		if !readable {
			return web.Fail(409, "原资料已撤权或清除")
		}
	}
	_, files, err := Inputs(ctx, tx, run)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if !slices.ContainsFunc(files, func(f InputSource) bool { return f.ID == issue.Source && f.Version == issue.Version }) {
			return web.Fail(409, "原资料无法读取或版本已变化")
		}
	}
	return nil
}

func ApplyColumnCorrections(ctx context.Context, tx pgx.Tx, run string, ordinal int, sources []InputSource) error {
	rows, err := tx.Query(ctx, `select q.mapping from call_reviews v cross join lateral column_correction_items(v.input_mapping) with ordinality as q(mapping,n)
 where v.run_id=$1 and v.ordinal=$2 and v.superseded_by is null order by v.attempt_no,q.n`, run, ordinal)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c ColumnCorrection
		if err := rows.Scan(&c); err != nil {
			return err
		}
		found := false
		for i := range sources {
			if sources[i].ID == c.Source && sources[i].Version == c.Version {
				if sources[i].ColumnMapping == nil {
					sources[i].ColumnMapping = map[string]string{}
				}
				for field, original := range c.Columns {
					sources[i].ColumnMapping[field] = original
				}
				found = true
				break
			}
		}
		if !found {
			return web.Fail(409, "字段对应所引用的原资料版本已变化")
		}
	}
	return rows.Err()
}
