package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/idna"
)

var ErrBlocked = errors.New("outbound destination is not permitted")

type Authority struct {
	Host      string
	Port      int
	plainHTTP bool
}

func hostIdentity(raw string) (string, error) {
	if ip, err := netip.ParseAddr(raw); err == nil && ip.Zone() == "" {
		return ip.Unmap().String(), nil
	}
	name, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(raw), "."))
	if err != nil || name == "" || len(name) > 253 {
		return "", ErrBlocked
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", ErrBlocked
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrBlocked
			}
		}
	}
	return name, nil
}

func parseAuthority(raw string, plainHTTP bool) (Authority, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(plainHTTP && u.Scheme == "http")) {
		return Authority{}, ErrBlocked
	}
	host, err := hostIdentity(u.Hostname())
	if err != nil {
		return Authority{}, err
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if value := u.Port(); value != "" {
		port, err = strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return Authority{}, ErrBlocked
		}
	}
	return Authority{Host: host, Port: port, plainHTTP: u.Scheme == "http"}, nil
}

type Policy struct {
	DB interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}
	Resolver interface {
		LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
	}
	Dial func(context.Context, string, string) (net.Conn, error)
}

func (p Policy) entry(ctx context.Context, authority Authority) ([]netip.Prefix, error) {
	if p.DB == nil {
		return nil, ErrBlocked
	}
	var raw []string
	err := p.DB.QueryRow(ctx, `select trusted_networks::text[] from outbound_host_allowlist
	 where (host,port)=($1,$2) and enabled`, authority.Host, authority.Port).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBlocked
	}
	if err != nil {
		return nil, err
	}
	return parseTrustedNetworks(raw)
}

// Allowed reports whether the authority was approved. Client also validates
// its resolved addresses immediately before opening a connection.
func (p Policy) Allowed(ctx context.Context, raw string, plainHTTP bool) bool {
	authority, err := parseAuthority(raw, plainHTTP)
	if err != nil {
		return false
	}
	_, err = p.entry(ctx, authority)
	return err == nil
}

func (p Policy) resolve(ctx context.Context, authority Authority, trusted []netip.Prefix) ([]netip.Addr, error) {
	var addresses []netip.Addr
	if ip, err := netip.ParseAddr(authority.Host); err == nil {
		addresses = []netip.Addr{ip}
	} else {
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		var err error
		addresses, err = resolver.LookupNetIP(ctx, "ip", authority.Host)
		if err != nil || len(addresses) == 0 || len(addresses) > 32 {
			return nil, ErrBlocked
		}
	}
	for i := range addresses {
		addresses[i] = addresses[i].Unmap()
		if !permittedAddress(addresses[i], trusted) {
			return nil, ErrBlocked
		}
	}
	return addresses, nil
}

// Client uses a fresh, narrowly scoped transport. Dialing a numeric address
// from this DNS answer keeps the TLS host name and prevents a second lookup
// to a different address. Proxies and HTTP redirects cannot change the route.
func (p Policy) Client(ctx context.Context, raw string, plainHTTP bool, base *http.Client) (*http.Client, error) {
	authority, err := parseAuthority(raw, plainHTTP)
	if err != nil {
		return nil, err
	}
	trusted, err := p.entry(ctx, authority)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolve(ctx, authority, trusted)
	if err != nil {
		return nil, err
	}
	if authority.plainHTTP {
		for _, addr := range addresses {
			if !addr.IsPrivate() && !addr.IsLoopback() {
				return nil, ErrBlocked
			}
		}
	}
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	standard, ok := transport.(*http.Transport)
	if !ok {
		return nil, ErrBlocked
	}
	tr := standard.Clone()
	tr.Proxy, tr.DialTLS, tr.DialTLSContext = nil, nil, nil
	tr.DisableKeepAlives = true
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != strconv.Itoa(authority.Port) {
			return nil, ErrBlocked
		}
		identity, err := hostIdentity(host)
		if err != nil || identity != authority.Host {
			return nil, ErrBlocked
		}
		return p.dialResolved(ctx, network, port, addresses)
	}
	client := *base
	client.Transport = tr
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

// Connect opens a TCP connection to a resolved address approved by the same
// directory as the HTTP clients. The caller negotiates and verifies TLS.
func (p Policy) Connect(ctx context.Context, host string, port int) (net.Conn, error) {
	name, err := hostIdentity(host)
	if err != nil || port < 1 || port > 65535 {
		return nil, ErrBlocked
	}
	authority := Authority{Host: name, Port: port}
	trusted, err := p.entry(ctx, authority)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolve(ctx, authority, trusted)
	if err != nil {
		return nil, err
	}
	return p.dialResolved(ctx, "tcp", strconv.Itoa(port), addresses)
}

func (p Policy) dialResolved(ctx context.Context, network, port string, addresses []netip.Addr) (net.Conn, error) {
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	for _, ip := range addresses {
		if conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port)); err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("approved outbound host is unreachable")
}

var explicitNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("::1/128"),
}

var deniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

func parseTrustedNetworks(raw []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() {
			return nil, ErrBlocked
		}
		prefix = prefix.Masked()
		allowed := false
		for _, network := range explicitNetworks {
			if prefix.Bits() >= network.Bits() && network.Contains(prefix.Addr()) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrBlocked
		}
		out = append(out, prefix)
	}
	return out, nil
}

func permittedAddress(addr netip.Addr, trusted []netip.Prefix) bool {
	if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.Zone() != "" {
		return false
	}
	for _, network := range deniedNetworks {
		if network.Contains(addr) {
			return false
		}
	}
	if addr.IsPrivate() || addr.IsLoopback() {
		for _, network := range trusted {
			if network.Contains(addr) {
				return true
			}
		}
		return false
	}
	return addr.IsGlobalUnicast()
}
