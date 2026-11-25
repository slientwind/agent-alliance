package egress

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportLegacy hosts is a one-time migration for installations that already
// trusted an external authority. Private DNS names still require an explicit
// network approval. An already approved literal internal IP stays exact.
func ImportLegacy(ctx context.Context, db *pgxpool.Pool, configured string) error {
	if strings.TrimSpace(configured) == "" {
		return nil
	}
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var first bool
		err := tx.QueryRow(ctx, `insert into outbound_host_imported default values on conflict do nothing returning id`).Scan(&first)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, raw := range strings.Split(configured, ",") {
			raw = strings.TrimSpace(raw)
			if strings.ContainsAny(raw, "/?@#") {
				continue
			}
			authority, err := parseAuthority("https://"+raw, false)
			if err != nil {
				continue
			}
			networks := []string{}
			if ip, err := netip.ParseAddr(authority.Host); err == nil && (ip.IsPrivate() || ip.IsLoopback()) {
				prefix := netip.PrefixFrom(ip, ip.BitLen())
				if permittedAddress(ip, []netip.Prefix{prefix}) {
					networks = append(networks, prefix.String())
				}
			}
			if _, err := tx.Exec(ctx, `insert into outbound_host_allowlist(host,port,trusted_networks)
			 values($1,$2,$3::text[]::cidr[]) on conflict do nothing`, authority.Host, authority.Port, networks); err != nil {
				return err
			}
		}
		return nil
	})
}
