package mailrelay

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmailAddressAndMIMEHeaders(t *testing.T) {
	for _, value := range []string{"Recipient <r@example.test>", "valid@example.test\r\nBcc: victim@example.test", "missing-domain"} {
		if ValidAddress(value) {
			t.Fatalf("unsafe SMTP recipient accepted: %q", value)
		}
	}
	if !ValidAddress("member+notification@example.test") {
		t.Fatal("valid alias rejected")
	}
	id, raw, err := compose("sender@example.test", "reader@example.test", "stable-id", "https://workspace.example.test", "project",
		time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC))
	if err != nil || id != "<stable-id@example.test>" || !strings.Contains(string(raw), "https://workspace.example.test/chat/project") ||
		!strings.Contains(string(raw), "\r\nTo: reader@example.test\r\n") {
		t.Fatalf("notification MIME invalid: %s %q %v", id, raw, err)
	}
}

func TestMailRelayUsesApprovedTLSAndPersistsDeliveryConfirmation(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("d8", 32))
	if err != nil {
		t.Fatal(err)
	}
	admin := identity.Member{ID: uuid.NewString()}
	if _, err := db.Exec(ctx, `insert into members(id,email,display_name,password_hash,role) values($1,$2,'SMTP admin','','admin')`,
		admin.ID, admin.ID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewTLSServer(http.NotFoundHandler())
	roots := x509.NewCertPool()
	roots.AddCert(issuer.Certificate())
	keys := issuer.TLS.Certificates
	issuer.Close()

	relay := Relay{DB: db, Vault: vault, Roots: roots, Egress: egress.Policy{DB: db}, Public: "https://workspace.example.test"}
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			host, rawPort, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(rawPort)
			if err != nil {
				t.Fatal(err)
			}
			var delivered atomic.Int32
			go func() {
				for {
					client, err := listener.Accept()
					if err != nil {
						return
					}
					go fakeSMTP(t, client, &tls.Config{Certificates: keys, MinVersion: tls.VersionTLS12}, mode, &delivered)
				}
			}()
			if _, err := db.Exec(ctx, `insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])`,
				host, port); err != nil {
				t.Fatal(err)
			}
			initial, err := relay.current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"host":%q,"port":%d,"security":%q,"sender":"sender@example.test","username":"fixture-user",
				"password":"fixture-key","enabled":true,"expected":%d}`, host, port, mode, initial.Revision)
			request := httptest.NewRequestWithContext(ctx, "PUT", "/", strings.NewReader(body))
			if err := relay.configure(httptest.NewRecorder(), request, admin); err != nil {
				t.Fatal(err)
			}
			status := httptest.NewRecorder()
			if err := relay.probe(status, httptest.NewRequestWithContext(ctx, "POST", "/", nil), admin); err != nil ||
				!strings.Contains(status.Body.String(), "ready") || delivered.Load() != 0 {
				t.Fatalf("authenticated connection probe: %s %v", status.Body, err)
			}
			id := uuid.NewString()
			receipt, err := relay.Send(ctx, id, "reader@example.test", "project-fixture", func(context.Context) bool { return true })
			if err != nil || receipt != "<"+id+"@example.test>" || delivered.Load() != 1 {
				t.Fatalf("mail acceptance: %q count=%d %v", receipt, delivered.Load(), err)
			}
			if _, err := relay.Send(ctx, id, "reader@example.test", "project-fixture", func(context.Context) bool { return false }); !errors.Is(err, ErrRevoked) ||
				delivered.Load() != 1 {
				t.Fatalf("revoked mail escaped: %v %d", err, delivered.Load())
			}
			if _, err := db.Exec(ctx, `update outbound_host_allowlist set enabled=false where (host,port)=($1,$2)`, host, port); err != nil {
				t.Fatal(err)
			}
			if _, err := relay.Send(ctx, id, "reader@example.test", "project-fixture", func(context.Context) bool { return true }); !errors.Is(err, ErrUnavailable) ||
				delivered.Load() != 1 {
				t.Fatalf("blocked destination received mail: %v %d", err, delivered.Load())
			}
			w := httptest.NewRecorder()
			if err := relay.settings(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil), admin); err != nil ||
				strings.Contains(w.Body.String(), "fixture-key") || !strings.Contains(w.Body.String(), `"hasPassword":true`) {
				t.Fatal("mail settings expose secrets or omit credential status")
			}
		})
	}
}

func fakeSMTP(t *testing.T, conn net.Conn, tlsConfig *tls.Config, security string, delivered *atomic.Int32) {
	t.Helper()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	secured := security == "tls"
	if secured {
		conn = tls.Server(conn, tlsConfig)
	}
	reader := bufio.NewReader(conn)
	write := func(value string) { _, _ = io.WriteString(conn, value) }
	write("220 fixture SMTP ready\r\n")
	authed := false
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "EHLO "):
			if secured {
				write("250-fixture\r\n250 AUTH PLAIN\r\n")
			} else {
				write("250-fixture\r\n250 STARTTLS\r\n")
			}
		case line == "STARTTLS" && !secured:
			write("220 Start TLS\r\n")
			conn = tls.Server(conn, tlsConfig)
			reader = bufio.NewReader(conn)
			secured = true
		case strings.HasPrefix(line, "AUTH PLAIN ") && secured:
			clear, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			if err == nil && string(clear) == "\x00fixture-user\x00fixture-key" {
				authed = true
				write("235 Authenticated\r\n")
			} else {
				write("535 Authentication refused\r\n")
			}
		case line == "MAIL FROM:<sender@example.test>" && authed, line == "RCPT TO:<reader@example.test>" && authed:
			write("250 OK\r\n")
		case line == "DATA" && authed:
			write("354 End with dot\r\n")
			var message strings.Builder
			for {
				part, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if part == ".\r\n" {
					break
				}
				message.WriteString(part)
			}
			if !strings.Contains(message.String(), "Message-ID:") || !strings.Contains(message.String(), "workspace.example.test/chat/project-fixture") {
				t.Errorf("fixture received an invalid notification: %s", message.String())
			}
			delivered.Add(1)
			write("250 queued\r\n")
		case line == "QUIT":
			write("221 closing\r\n")
			return
		default:
			write("554 Command rejected\r\n")
		}
	}
}
