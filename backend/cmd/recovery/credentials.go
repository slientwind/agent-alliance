package main

import (
	"alliance/internal/resources"
	"context"
	"github.com/jackc/pgx/v5"
)

type keyReport struct {
	Status                   string
	Stored, Verified, Failed int
}

// Test authenticated decryption with the original scope. Never emit plaintext,
// credential IDs or the supplied key, and never call the upstream provider.
func checkKeys(ctx context.Context, conn *pgx.Conn, path string) (keyReport, error) {
	report := keyReport{Status: "not_provided"}
	var vault resources.Vault
	if path != "" {
		raw, err := key(path)
		if err != nil {
			return report, err
		}
		vault, err = resources.NewVault(raw)
		if err != nil {
			return report, err
		}
	}
	rows, err := conn.Query(ctx, `select 'model/'||resource_id||'/'||revision,encrypted_key from model_revisions
 union all select 'agent/'||agent_id||'/'||revision,key_override from agent_revisions where key_override is not null
 union all select 'candidate/'||id,encrypted_key from model_key_candidates where encrypted_key is not null`)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		var encrypted []byte
		if err = rows.Scan(&scope, &encrypted); err != nil {
			return report, err
		}
		report.Stored++
		if path == "" {
			continue
		}
		if _, err := vault.Open(encrypted, scope); err != nil {
			report.Failed++
		} else {
			report.Verified++
		}
	}
	switch {
	case report.Stored == 0:
		report.Status = "no_stored_credentials"
	case report.Failed > 0:
		report.Status = "mismatch"
	case report.Verified == report.Stored:
		report.Status = "verified"
	}
	return report, rows.Err()
}
