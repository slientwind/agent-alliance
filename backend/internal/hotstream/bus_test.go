package hotstream_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/hotstream"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestMemoryAndRelayReplayUTF8DraftOffsets(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	bus := hotstream.NewMemory()
	routes := http.NewServeMux()
	routes.Handle("POST "+hotstream.RelayPath, hotstream.RelayHandler(bus, "fixture-relay-access"))
	server := httptest.NewServer(routes)
	defer server.Close()
	project, run := uuid.NewString(), uuid.NewString()
	body := strings.Repeat("报表🟩\n", 600)
	frame := hotstream.Event{Kind: hotstream.Draft, Run: run, Step: 1, Attempt: 1, Control: 2}
	wrong, err := hotstream.OpenRelay(server.URL+hotstream.RelayPath, "fixture-wrong")
	if err != nil {
		t.Fatal(err)
	}
	frame.Text = "hidden"
	if err := wrong.Publish(ctx, project, frame); err == nil {
		t.Fatal("untrusted publisher reached the project stream")
	}
	relay, err := hotstream.OpenRelay(server.URL+hotstream.RelayPath, "fixture-relay-access")
	if err != nil {
		t.Fatal(err)
	}
	emit, flush := hotstream.DraftEmitter(ctx, relay, project, frame)
	emit(body)
	flush()
	read, err := bus.Read(ctx, project, "")
	if err != nil || len(read) < 2 {
		t.Fatalf("did not replay the batched live draft: %d %v", len(read), err)
	}
	var replay strings.Builder
	for _, item := range read {
		if item.Offset != replay.Len() || item.Run != run || item.Control != 2 {
			t.Fatalf("incorrect replay offset or context: %d", item.Offset)
		}
		replay.WriteString(item.Text)
	}
	if replay.String() != body {
		t.Fatal("UTF-8 text changed when replayed")
	}
	if rest, err := bus.Read(ctx, project, read[len(read)-1].ID); err != nil || len(rest) != 0 {
		t.Fatalf("already received events were returned again: %d %v", len(rest), err)
	}
}

func TestRedisStreamsFanoutKeepsDraftContentSealed(t *testing.T) {
	address := os.Getenv("ALLIANCE_HOT_STREAM_TEST_REDIS")
	if address == "" {
		t.Skip("set ALLIANCE_HOT_STREAM_TEST_REDIS to the scratch Redis server")
	}
	vault, err := resources.NewVault(strings.Repeat("e7", 32))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := hotstream.OpenRedis(address, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := hotstream.OpenRedis(address, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	project, run := uuid.NewString(), uuid.NewString()
	const text = "fixture-visible-prompt-fragment"
	if err := writer.Publish(ctx, project, hotstream.Event{Kind: hotstream.Draft, Run: run, Step: 2, Attempt: 1, Text: text}); err != nil {
		t.Fatal("could not publish to the scratch Redis stream")
	}
	options, err := redis.ParseURL(address)
	if err != nil {
		t.Fatal("invalid scratch Redis address")
	}
	client := redis.NewClient(options)
	defer client.Close()
	key := "alliance:hot:project:" + project
	defer client.Del(context.Background(), key)
	stored, err := client.XRange(ctx, key, "-", "+").Result()
	if err != nil || len(stored) != 1 || strings.Contains(stored[0].Values["sealed"].(string), text) {
		t.Fatal("missing or plaintext Redis record")
	}
	for _, subscriber := range []hotstream.HotStreamBus{writer, reader} {
		items, err := subscriber.Read(ctx, project, "")
		if err != nil || len(items) != 1 || items[0].Text != text || items[0].Run != run || items[0].ID != stored[0].ID {
			t.Fatal("independent reader could not replay the sealed event")
		}
	}
	checkPresence(t, writer, reader)
	member, expiredTab := uuid.NewString(), uuid.NewString()
	presenceKey := key + ":presence"
	defer client.Del(context.Background(), presenceKey)
	if err := client.ZAdd(ctx, presenceKey, redis.Z{Score: 1, Member: member + "/" + expiredTab}).Err(); err != nil {
		t.Fatal("could not seed an expired Redis presence lease")
	}
	if active, err := reader.OnlineMembers(ctx, project); err != nil || len(active) != 0 {
		t.Fatal("an expired Redis lease still counted as an online member")
	}
}

func TestMemoryPresenceKeepsTheMemberUntilTheirLastTabCloses(t *testing.T) {
	bus := hotstream.NewMemory()
	checkPresence(t, bus, bus)
}

func checkPresence(t *testing.T, writer, reader hotstream.HotStreamBus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	project, member, first, second := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, tab := range []string{first, second} {
		if err := writer.KeepPresence(ctx, project, member, tab); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int) {
		t.Helper()
		active, err := reader.OnlineMembers(ctx, project)
		if err != nil || len(active) != want || want != 0 && active[0] != member {
			t.Fatalf("got presence %v, want %d unique members: %v", active, want, err)
		}
	}
	check(1)
	if err := reader.RemovePresence(ctx, project, member, first); err != nil {
		t.Fatal(err)
	}
	check(1)
	if err := writer.RemovePresence(ctx, project, member, second); err != nil {
		t.Fatal(err)
	}
	check(0)
}
