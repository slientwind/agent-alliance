package channels

import (
	"context"
	"net/url"
	"testing"
)

func TestDingTalkCredentialDestinations(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://oapi.dingtalk.com/robot/sendBySession?session=fixture", true},
		{"https://oapi.dingtalk.com:443/robot/sendBySession?session=fixture", true},
		{"http://oapi.dingtalk.com/robot/sendBySession?session=fixture", false},
		{"https://oapi.dingtalk.com.evil.invalid/robot/sendBySession?session=fixture", false},
		{"https://oapi.dingtalk.com@evil.invalid/robot/sendBySession?session=fixture", false},
		{"https://oapi.dingtalk.com:444/robot/sendBySession?session=fixture", false},
		{"https://oapi.dingtalk.com/robot/send?session=fixture", false},
		{"https://oapi.dingtalk.com/robot/sendBySession?session=fixture&session=other", false},
	} {
		if got := validDingReplyURL(tc.url); got != tc.ok {
			t.Errorf("reply destination %q accepted=%t, want=%t", tc.url, got, tc.ok)
		}
	}
	trusted, err := dingStreamURL("wss://wss-open-connection.dingtalk.com:443/connect", "one+two/three=")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(trusted)
	if err != nil || parsed.Query().Get("ticket") != "one+two/three=" {
		t.Fatalf("invalid Stream ticket encoding: %s %v", trusted, err)
	}
	for _, endpoint := range []string{
		"ws://wss-open-connection.dingtalk.com/connect",
		"wss://wss-open-connection.dingtalk.com.evil.invalid/connect",
		"wss://wss-open-connection.dingtalk.com/connect?ticket=old",
	} {
		if _, err := dingStreamURL(endpoint, "fixture"); err == nil {
			t.Errorf("invalid Stream endpoint accepted: %s", endpoint)
		}
	}
}

func TestDingTalkStreamHeartbeatDoesNotNeedBusinessState(t *testing.T) {
	var frame dingStreamFrame
	frame.Type, frame.Headers.Topic, frame.Headers.MessageID, frame.Data = "SYSTEM", "ping", "heartbeat-1", `{"opaque":"fixture"}`
	d := DingTalk{}
	if ack := d.handleStreamFrame(context.Background(), frame); ack == nil || ack.Code != 200 ||
		ack.Headers["messageId"] != "heartbeat-1" || ack.Data != frame.Data {
		t.Fatalf("heartbeat acknowledgement: %+v", ack)
	}
	frame.Type = "EVENT"
	if ack := d.handleStreamFrame(context.Background(), frame); ack == nil || ack.Code != 404 {
		t.Fatalf("unexpected subscription acknowledgement: %+v", ack)
	}
}
