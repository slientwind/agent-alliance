package channels

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeishuNoticeRequestRetainsProviderReceipt(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open-apis/im/v1/messages" || r.URL.Query().Get("receive_id_type") != "open_id" ||
			r.Header.Get("Authorization") != "Bearer local-fixture" {
			t.Errorf("unexpected Feishu destination or bearer")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var message map[string]string
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if message["uuid"] != "automation-receipt" || message["receive_id"] != "fixture-user" ||
			message["msg_type"] != "text" || !strings.Contains(message["content"], "定时委托已完成") {
			t.Errorf("unexpected Feishu notice: %+v", message)
		}
		if message["content"] == `{"text":"定时委托已完成，确认已丢失"}` {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"fixture-receipt"}}`)
	}))
	defer fixture.Close()
	original := feishuOrigin
	feishuOrigin = fixture.URL
	defer func() { feishuOrigin = original }()
	const path = "im/v1/messages?receive_id_type=open_id"
	response, err := postFeishuText(t.Context(), "local-fixture", path, "automation-receipt", "定时委托已完成", "fixture-user")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := response["data"].(map[string]any); data["message_id"] != "fixture-receipt" {
		t.Fatalf("upstream receipt missing: %+v", response)
	}
	_, err = postFeishuText(t.Context(), "local-fixture", path, "automation-receipt", "定时委托已完成，确认已丢失", "fixture-user")
	if !errors.Is(err, errFeishuUnconfirmed) {
		t.Fatalf("uncertain upstream outcome became a definitive failure: %v", err)
	}
}
