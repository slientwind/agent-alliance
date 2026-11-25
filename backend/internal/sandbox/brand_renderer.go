package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"alliance/internal/brandkit"
	"alliance/internal/knowledge"
)

type BrandRenderer struct {
	Pool
	Objects knowledge.Objects
	Root    string
	Socket  string
}

var brandDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r BrandRenderer) Render(ctx context.Context, template brandkit.Template, fields map[string]string) ([]byte, error) {
	if r.Root == "" || r.Socket == "" || !brandDigest.MatchString(template.Hash) {
		return nil, errors.New("official template renderer is unavailable")
	}
	var result []byte
	err := r.withSlot(ctx, func(job context.Context, limit time.Duration) error {
		request, cancel := context.WithTimeout(job, limit)
		defer cancel()
		if err := r.stage(request, template.Hash); err != nil {
			return err
		}
		var err error
		result, err = r.invoke(request, template, fields, limit)
		return err
	})
	return result, err
}

func (r BrandRenderer) stage(ctx context.Context, hash string) error {
	source, err := r.Objects.Open(hash)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(r.Root, 0750); err != nil {
		return err
	}
	target := filepath.Join(r.Root, hash)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := os.CreateTemp(r.Root, ".template-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, digest), io.LimitReader(source, brandkit.MaxTemplateBytes+1))
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if n <= 0 || n > brandkit.MaxTemplateBytes || fmt.Sprintf("%x", digest.Sum(nil)) != hash {
		return errors.New("official template file has changed")
	}
	if err := out.Chmod(0444); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Link(out.Name(), target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func (r BrandRenderer) invoke(ctx context.Context, template brandkit.Template, fields map[string]string, limit time.Duration) ([]byte, error) {
	if template.Slots == nil {
		template.Slots = []string{}
	}
	payload, err := json.Marshal(map[string]any{
		"digest": template.Hash, "format": template.Format, "fields": fields, "slots": template.Slots,
	})
	if err != nil || len(payload) > 256<<10 {
		return nil, errors.New("invalid official template input")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://brand-renderer/render", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Render-Seconds", fmt.Sprintf("%d", int(limit.Seconds())))
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", r.Socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	reply, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return nil, errors.New("content does not match the official template")
	}
	raw, err := io.ReadAll(io.LimitReader(reply.Body, brandkit.MaxTemplateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > brandkit.MaxTemplateBytes || knowledge.ValidateOffice(bytes.NewReader(raw), int64(len(raw)), knowledge.OfficePart("result."+template.Format)) != nil {
		return nil, errors.New("template renderer produced an invalid Office file")
	}
	return raw, nil
}
