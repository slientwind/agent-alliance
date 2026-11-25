package hotstream

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"alliance/internal/web"
)

const RelayPath = "/internal/project-stream"

type Relay struct {
	address, token string
	client         *http.Client
}

type envelope struct {
	Project string `json:"project"`
	Event   Event  `json:"event"`
}

func OpenRelay(address, token string) (*Relay, error) {
	target, err := url.Parse(address)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(target.Scheme != "https" && target.Scheme != "http") || target.Path != RelayPath || token == "" {
		return nil, errors.New("project stream relay URL or connector token is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Relay{address, token, &http.Client{Timeout: time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (relay *Relay) Publish(ctx context.Context, project string, event Event) error {
	if err := validate(project, event); err != nil {
		return err
	}
	body, err := json.Marshal(envelope{project, event})
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, relay.address, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+relay.token)
	r.Header.Set("Content-Type", "application/json")
	response, err := relay.client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("project stream relay rejected the event")
	}
	return nil
}

func RelayHandler(bus Publisher, token string) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			return web.Fail(403, "此服务仅供内部调用")
		}
		var in envelope
		if err := web.ReadLimited(w, r, &in, 32<<10); err != nil {
			return err
		}
		if in.Event.Kind != Draft && in.Event.Kind != Thought {
			return web.Fail(422, "此处只接收运行进度")
		}
		if err := bus.Publish(r.Context(), in.Project, in.Event); err != nil {
			if errors.Is(err, ErrEvent) {
				return web.Fail(422, "运行进度格式无效")
			}
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
