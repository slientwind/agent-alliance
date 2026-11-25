package mailrelay

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"alliance/internal/egress"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnavailable = errors.New("邮件服务暂不可用")
	ErrUnconfirmed = errors.New("邮件接收状态待确认")
	ErrRevoked     = errors.New("邮件接收授权已失效")
)

type Relay struct {
	DB     *pgxpool.Pool
	Vault  resources.Vault
	Egress egress.Policy
	Roots  *x509.CertPool
	Public string
}

type config struct {
	Host, Security, Sender, Username string
	Port, Revision                   int
	Password                         []byte
	Enabled                          bool
}

func credentialScope(c config) string {
	return fmt.Sprintf("mail-relay/%s/%d/%s/%s", c.Host, c.Port, c.Security, c.Username)
}

func ValidAddress(value string) bool {
	if value == "" || len(value) > 254 || strings.ContainsFunc(value, func(c rune) bool { return c <= ' ' || c > '~' }) {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value
}

func (r Relay) current(ctx context.Context) (config, error) {
	var out config
	err := r.DB.QueryRow(ctx, `select host,port,security,sender,username,encrypted_password,enabled,revision from mail_relay`).
		Scan(&out.Host, &out.Port, &out.Security, &out.Sender, &out.Username, &out.Password, &out.Enabled, &out.Revision)
	return out, err
}

func (r Relay) connect(ctx context.Context, c config, allowed func() bool) (*smtp.Client, error) {
	conn, err := r.Egress.Connect(ctx, c.Host, c.Port)
	if err != nil {
		return nil, ErrUnavailable
	}
	failed := func() (*smtp.Client, error) {
		conn.Close()
		return nil, ErrUnavailable
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: c.Host, RootCAs: r.Roots, MinVersion: tls.VersionTLS12}
	if c.Security == "tls" {
		secured := tls.Client(conn, tlsConfig)
		if err := secured.HandshakeContext(ctx); err != nil {
			return failed()
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return failed()
	}
	if c.Security == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return failed()
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || len(state.VerifiedChains) == 0 {
		return failed()
	}
	if allowed != nil && !allowed() {
		conn.Close()
		return nil, ErrRevoked
	}
	if c.Username != "" {
		key, err := r.Vault.Open(c.Password, credentialScope(c))
		if err != nil || client.Auth(smtp.PlainAuth("", c.Username, key, c.Host)) != nil {
			return failed()
		}
	}
	return client, nil
}

func (r Relay) Send(ctx context.Context, notice, recipient, project string, allowed func(context.Context) bool) (string, error) {
	if _, err := uuid.Parse(notice); err != nil || !ValidAddress(recipient) {
		return "", ErrRevoked
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := r.current(ctx)
	if err != nil || !c.Enabled || !ValidAddress(c.Sender) {
		return "", ErrUnavailable
	}
	check := func() bool {
		var enabled bool
		err := r.DB.QueryRow(ctx, "select enabled from mail_relay where revision=$1", c.Revision).Scan(&enabled)
		return err == nil && enabled && allowed != nil && allowed(ctx)
	}
	if !check() {
		return "", ErrRevoked
	}
	client, err := r.connect(ctx, c, check)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if client.Mail(c.Sender) != nil || client.Rcpt(recipient) != nil {
		return "", ErrUnavailable
	}
	if !check() {
		return "", ErrRevoked
	}
	id, message, err := compose(c.Sender, recipient, notice, r.Public, project, time.Now())
	if err != nil {
		return "", ErrUnavailable
	}
	writer, err := client.Data()
	if err != nil {
		return "", ErrUnavailable
	}
	if _, err := writer.Write(message); err != nil {
		return "", ErrUnconfirmed
	}
	if err := writer.Close(); err != nil {
		var status *textproto.Error
		if errors.As(err, &status) {
			return "", ErrUnavailable
		}
		return "", ErrUnconfirmed
	}
	return id, nil
}

func compose(sender, recipient, id, public, project string, now time.Time) (string, []byte, error) {
	_, domain, _ := strings.Cut(sender, "@")
	messageID := "<" + id + "@" + domain + ">"
	var output bytes.Buffer
	fmt.Fprintf(&output, "From: %s\r\nTo: %s\r\nDate: %s\r\nMessage-ID: %s\r\nSubject: %s\r\n",
		sender, recipient, now.Format(time.RFC1123Z), messageID, mime.BEncoding.Encode("UTF-8", "定时委托已完成"))
	output.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	text := "你的定时委托已完成。登录企业工作区查看结果。"
	if endpoint, err := url.Parse(public); err == nil && endpoint.Scheme == "https" && endpoint.Hostname() != "" &&
		endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == "" {
		text += "\r\n" + strings.TrimRight(endpoint.String(), "/") + "/chat/" + url.PathEscape(project)
	}
	writer := quotedprintable.NewWriter(&output)
	if _, err := writer.Write([]byte(text + "\r\n")); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return messageID, output.Bytes(), nil
}
