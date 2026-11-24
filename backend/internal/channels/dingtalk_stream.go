package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const dingGateway = "https://api.dingtalk.com/v1.0/gateway/connections/open"
const dingBotTopic = "/v1.0/im/bot/messages/get"

type dingStreamFrame struct {
	Version string `json:"specVersion"`
	Type    string `json:"type"`
	Headers struct {
		MessageID   string `json:"messageId"`
		Topic       string `json:"topic"`
		ContentType string `json:"contentType"`
	} `json:"headers"`
	Data string `json:"data"`
}

type dingStreamAck struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

func (d DingTalk) streamEndpoint(ctx context.Context) (string, error) {
	type subscription struct {
		Type  string `json:"type"`
		Topic string `json:"topic"`
	}
	open := struct {
		ClientID      string         `json:"clientId"`
		ClientSecret  string         `json:"clientSecret"`
		Subscriptions []subscription `json:"subscriptions"`
	}{d.app, d.secret, []subscription{{Type: "CALLBACK", Topic: dingBotTopic}}}
	body, err := json.Marshal(open)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dingGateway, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("DingTalk gateway refused Stream registration")
	}
	var reply struct{ Endpoint, Ticket string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&reply); err != nil {
		return "", err
	}
	return dingStreamURL(reply.Endpoint, reply.Ticket)
}

func dingStreamURL(endpoint, ticket string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "wss" || !strings.EqualFold(u.Hostname(), "wss-open-connection.dingtalk.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery ||
		!dingOpaqueID.MatchString(ticket) {
		return "", errors.New("invalid DingTalk Stream endpoint or ticket")
	}
	u.RawQuery = url.Values{"ticket": {ticket}}.Encode()
	return u.String(), nil
}

func (d DingTalk) listen(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := d.listenConnection(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// A transport error may contain its temporary ticket. Keep it out of logs.
			slog.Warn("DingTalk Stream interrupted")
		}
		if time.Since(started) >= 30*time.Second {
			backoff = time.Second
		}
		wait := time.NewTimer(backoff + rand.N(backoff/4))
		select {
		case <-ctx.Done():
			wait.Stop()
			return
		case <-wait.C:
		}
		backoff = min(2*backoff, time.Minute)
	}
}

func (d DingTalk) listenConnection(ctx context.Context) error {
	endpoint, err := d.streamEndpoint(ctx)
	if err != nil {
		return err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	ws, response, err := d.dialer.DialContext(connectCtx, endpoint, nil)
	cancel()
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return err
	}
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	const readIdle = 90 * time.Second
	ws.SetReadLimit(128 << 10)
	refreshRead := func() error { return ws.SetReadDeadline(time.Now().Add(readIdle)) }
	ws.SetPongHandler(func(string) error { return refreshRead() })
	if err := refreshRead(); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(2*time.Second)); err != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()
	for {
		kind, raw, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if err := refreshRead(); err != nil {
			return err
		}
		var frame dingStreamFrame
		if kind != websocket.TextMessage || json.Unmarshal(raw, &frame) != nil {
			continue
		}
		if frame.Type == "SYSTEM" && frame.Headers.Topic == "disconnect" {
			return nil
		}
		ack := d.handleStreamFrame(ctx, frame)
		if ack == nil {
			continue
		}
		if err := ws.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if err := ws.WriteJSON(ack); err != nil {
			return err
		}
	}
}

func (d DingTalk) handleStreamFrame(ctx context.Context, frame dingStreamFrame) *dingStreamAck {
	if !dingOpaqueID.MatchString(frame.Headers.MessageID) {
		return nil
	}
	ack := &dingStreamAck{Code: 200, Message: "OK", Headers: map[string]string{
		"messageId": frame.Headers.MessageID, "contentType": "application/json",
	}, Data: `{"response":null}`}
	switch {
	case frame.Type == "SYSTEM" && frame.Headers.Topic == "ping":
		ack.Data = frame.Data
	case frame.Type == "CALLBACK" && frame.Version == "1.0" && frame.Headers.Topic == dingBotTopic &&
		(frame.Headers.ContentType == "application/json" || frame.Headers.ContentType == ""):
		if err := d.acceptCallback(ctx, frame.Headers.MessageID, frame.Data); err != nil {
			ack.Code, ack.Message = 500, "Channel queue unavailable"
		}
	default:
		ack.Code, ack.Message = 404, "Unsupported frame"
	}
	return ack
}
