package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5"
)

type entryFixture func(string, int) ([]string, error)
type fixtureRow struct {
	networks []string
	err      error
}

func (f fixtureRow) Scan(dest ...any) error {
	*(dest[0].(*[]string)) = f.networks
	return f.err
}

func (f entryFixture) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	list, err := f(args[0].(string), args[1].(int))
	return fixtureRow{list, err}
}

type resolverFixture func(string) ([]netip.Addr, error)

func (f resolverFixture) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f(host)
}

func TestEgressUsesOneApprovedDNSAnswerAndPinnedDial(t *testing.T) {
	var lookups, dials int
	policy := Policy{
		DB: entryFixture(func(host string, port int) ([]string, error) {
			if host != "approved.example" || port != 8443 {
				return nil, pgx.ErrNoRows
			}
			return nil, nil
		}),
		Resolver: resolverFixture(func(host string) ([]netip.Addr, error) {
			lookups++
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dials++
			if address != "93.184.216.34:8443" {
				t.Fatalf("expected the validated numeric address, got %s", address)
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}
	client, err := policy.Client(t.Context(), "https://Approved.Example.:8443/service", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	tr := client.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.DialTLSContext != nil {
		t.Fatal("proxy or dedicated TLS transport bypasses the authorized dial")
	}
	conn, err := tr.DialContext(t.Context(), "tcp", "approved.example.:8443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := tr.DialContext(t.Context(), "tcp", "other.example:8443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("dial changed host: %v", err)
	}
	if lookups != 1 || dials != 1 || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatalf("lookups=%d dials=%d, unexpected redirect policy", lookups, dials)
	}
	if _, err := policy.Client(t.Context(), "https://approved.example:443/service", false, nil); !errors.Is(err, ErrBlocked) {
		t.Fatalf("unapproved port: %v", err)
	}
}

func TestEgressRejectsUnapprovedInternalAddressesAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		addresses []string
		trusted   []string
		permit    bool
	}{
		{[]string{"93.184.216.34"}, nil, true},
		{[]string{"93.184.216.34", "::ffff:10.0.1.5"}, nil, false},
		{[]string{"10.0.1.5"}, []string{"10.0.1.0/24"}, true},
		{[]string{"10.0.2.5"}, []string{"10.0.1.0/24"}, false},
		{[]string{"fd00:ec2::254"}, []string{"fc00::/7"}, false},
		{[]string{"169.254.169.254"}, nil, false},
		{[]string{"127.0.0.1"}, nil, false},
		{[]string{"127.0.0.1"}, []string{"127.0.0.1/32"}, true},
	} {
		policy := Policy{DB: entryFixture(func(string, int) ([]string, error) { return tc.trusted, nil }),
			Resolver: resolverFixture(func(string) ([]netip.Addr, error) {
				var out []netip.Addr
				for _, ip := range tc.addresses {
					out = append(out, netip.MustParseAddr(ip))
				}
				return out, nil
			})}
		client, err := policy.Client(t.Context(), "https://approved.example", false, nil)
		if (err == nil) != tc.permit {
			t.Errorf("%v trusted=%v: err=%v want permit=%t", tc.addresses, tc.trusted, err, tc.permit)
		}
		if client != nil {
			client.CloseIdleConnections()
		}
	}
	for _, invalid := range []string{"0.0.0.0/0", "169.254.169.254/32", "93.184.216.34/32", "::ffff:10.0.0.0/104"} {
		if _, err := parseTrustedNetworks([]string{invalid}); err == nil {
			t.Errorf("accepted invalid internal rule %s", invalid)
		}
	}
}
