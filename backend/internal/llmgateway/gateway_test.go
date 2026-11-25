package llmgateway

import (
	"alliance/internal/resources"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixtureCatalog []Candidate

func (f fixtureCatalog) ModelsForTier(context.Context, Tier, time.Time) ([]Candidate, error) {
	return f, nil
}

type fixtureCaller func(context.Context, Candidate, Request) (Result, error)

func (f fixtureCaller) Call(ctx context.Context, model Candidate, in Request) (Result, error) {
	return f(ctx, model, in)
}

type fixtureBudget struct {
	seen     map[string]bool
	settled  []string
	released []string
}

func (b *fixtureBudget) Reserve(_ context.Context, in Reservation) (bool, error) {
	if b.seen[in.OperationKey] {
		return false, nil
	}
	b.seen[in.OperationKey] = true
	return true, nil
}

func (b *fixtureBudget) Settle(_ context.Context, key string, _ Usage) (int64, error) {
	b.settled = append(b.settled, key)
	return 4, nil
}

func (b *fixtureBudget) Release(_ context.Context, key string) error {
	b.released = append(b.released, key)
	return nil
}

type fixtureResilience struct{ attempts []Attempt }

func (r *fixtureResilience) Acquire(_ context.Context, model Candidate, tokens int64, at time.Time) (Permit, error) {
	return Permit{ModelID: model.ModelID, Window: at, Tokens: tokens}, nil
}

func (r *fixtureResilience) Complete(_ context.Context, _ Permit, attempt Attempt) error {
	r.attempts = append(r.attempts, attempt)
	return nil
}

func TestGatewayFallsBackBeforeFirstTextAndDoesNotRetryDuplicateCharge(t *testing.T) {
	c := fixtureCatalog{
		{ModelID: "primary", PriceVersion: "price-primary", Capabilities: []string{"text"}},
		{ModelID: "backup", PriceVersion: "price-backup", Priority: 1, Capabilities: []string{"text"}},
	}
	b := &fixtureBudget{seen: make(map[string]bool)}
	state := &fixtureResilience{}
	g := Gateway{Catalog: c, Budget: b, Resilience: state, PrimaryWindow: 20 * time.Millisecond}
	var used []string
	var text strings.Builder
	g.Provider = fixtureCaller(func(ctx context.Context, m Candidate, in Request) (Result, error) {
		used = append(used, m.ModelID)
		if m.ModelID == "primary" {
			<-ctx.Done()
			in.EmitText("late primary")
			return Result{}, ProviderError{Retryable: true}
		}
		in.EmitText("backup")
		return Result{Reply: resources.Reply{Text: "backup"}, Usage: &Usage{PromptTokens: 2}}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	actor := CallIdentity{OperationKey: "call-one", TeamID: "team", MemberID: "member"}
	in := Request{Tier: Default, Messages: []resources.Message{{Role: "user", Content: "hello"}}, EmitText: func(value string) { text.WriteString(value) }}
	out, err := g.Call(ctx, actor, in)
	if err != nil || !out.Fallback || out.ModelID != "backup" || out.Cost == nil || *out.Cost != 4 || text.String() != "backup" {
		t.Fatalf("out=%+v err=%v visible=%s", out, err, text.String())
	}
	if len(b.settled) != 1 || len(b.released) != 0 || len(state.attempts) != 2 || !state.attempts[0].TransientFailure || !state.attempts[1].Fallback {
		t.Fatalf("accounting or circuit attempts incorrect: %+v %+v", b, state.attempts)
	}
	if _, err := g.Call(ctx, actor, in); !errors.Is(err, ErrModelSubmitted) || len(used) != 2 {
		t.Fatalf("duplicate submitted a provider call: %v %v", used, err)
	}
}

func TestGatewayKeepsOneProviderAfterStreamingStarts(t *testing.T) {
	used := 0
	g := Gateway{
		Catalog: fixtureCatalog{
			{ModelID: "primary", Capabilities: []string{"text"}},
			{ModelID: "backup", Capabilities: []string{"text"}, Priority: 1},
		}, Budget: &fixtureBudget{seen: make(map[string]bool)}, Resilience: &fixtureResilience{}, PrimaryWindow: 100 * time.Millisecond,
		Provider: fixtureCaller(func(_ context.Context, _ Candidate, in Request) (Result, error) {
			used++
			in.EmitText("partial")
			return Result{}, ProviderError{Retryable: true}
		}),
	}
	var text strings.Builder
	_, err := g.Call(t.Context(), CallIdentity{"operation", "team", "member"},
		Request{Tier: Default, Messages: []resources.Message{{Role: "user", Content: "hello"}}, EmitText: func(value string) { text.WriteString(value) }})
	if err == nil || used != 1 || text.String() != "partial" {
		t.Fatalf("used %d providers, visible %q, error %v", used, text.String(), err)
	}
}

func TestWeightedFallbackOrderNeverPreemptsPrimary(t *testing.T) {
	models := []Candidate{{ModelID: "equal-a", Priority: 1, Weight: 1}, {ModelID: "equal-b", Priority: 1, Weight: 9}, {ModelID: "primary"}}
	chosen := 0
	for key := range 100 {
		ordered := modelOrder(models, operationKey("test", string(rune(key))))
		if ordered[0].ModelID != "primary" {
			t.Fatal("weighted fallback preempted the primary")
		}
		if ordered[1].ModelID == "equal-b" {
			chosen++
		}
	}
	if chosen < 70 || chosen == 100 {
		t.Fatalf("higher weighted fallback chosen in %d of 100 operations", chosen)
	}
}
