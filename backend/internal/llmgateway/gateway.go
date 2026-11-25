package llmgateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoModelAvailable = errors.New("no compatible model is available in this tier")
	ErrModelSubmitted   = errors.New("model operation already submitted")
)

const primaryWindow = 2 * time.Second

type Gateway struct {
	Catalog interface {
		ModelsForTier(context.Context, Tier, time.Time) ([]Candidate, error)
	}
	Provider interface {
		Call(context.Context, Candidate, Request) (Result, error)
	}
	Budget interface {
		Reserve(context.Context, Reservation) (bool, error)
		Settle(context.Context, string, Usage) (int64, error)
		Release(context.Context, string) error
	}
	Resilience interface {
		Acquire(context.Context, Candidate, int64, time.Time) (Permit, error)
		Complete(context.Context, Permit, Attempt) error
	}
	// PrimaryWindow may shorten the first-attempt cutoff in tests or in a
	// deployment that requires faster handoff to a configured fallback.
	PrimaryWindow time.Duration
}

type CallIdentity struct {
	OperationKey string
	TeamID       string
	MemberID     string
	RunID        string
	AgentID      string
	Turn         int
}

type RoutedResult struct {
	Result
	ModelID   string
	ModelName string
	PriceID   string
	Fallback  bool
	Cost      *int64
}

func (g Gateway) Call(ctx context.Context, actor CallIdentity, in Request) (RoutedResult, error) {
	if actor.OperationKey == "" || actor.TeamID == "" || actor.MemberID == "" || !in.valid() {
		return RoutedResult{}, errors.New("invalid model gateway request")
	}
	models, err := g.Catalog.ModelsForTier(ctx, in.Tier, time.Now())
	if err != nil {
		return RoutedResult{}, err
	}
	order := modelOrder(models, actor.OperationKey)
	preferred := ""
	if len(order) != 0 {
		preferred = order[0].ModelID
	}
	order = slices.DeleteFunc(order, func(m Candidate) bool { return !in.supports(m) })
	var last error = ErrNoModelAvailable
	number := 0
	window := primaryWindow
	if g.PrimaryWindow > 0 && g.PrimaryWindow < window {
		window = g.PrimaryWindow
	}
	cutoff := time.Now().Add(window)
	for index, model := range order {
		estimate := in.estimate(model)
		permit, err := g.Resilience.Acquire(ctx, model, estimate.PromptTokens+estimate.CompletionTokens, time.Now())
		if err != nil {
			if !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrCircuitOpen) {
				return RoutedResult{}, err
			}
			last = err
			continue
		}
		key := operationKey(actor.OperationKey, model.ModelID)
		fresh, err := g.Budget.Reserve(ctx, Reservation{
			OperationKey: key, TeamID: actor.TeamID, MemberID: actor.MemberID, PriceID: model.PriceVersion, Tier: in.Tier, Estimate: estimate,
		})
		if err != nil || !fresh {
			done, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			finishErr := g.Resilience.Complete(done, permit, Attempt{Unsent: true})
			cancel()
			if finishErr != nil {
				return RoutedResult{}, errors.Join(err, finishErr)
			}
			if !fresh && err == nil {
				return RoutedResult{}, ErrModelSubmitted
			}
			if !errors.Is(err, ErrBudgetExceeded) {
				return RoutedResult{}, err
			}
			last = err
			continue
		}
		number++
		deadline := time.Time{}
		if index+1 < len(order) && time.Now().Before(cutoff) {
			deadline = cutoff
		}
		attempt := Attempt{OperationKey: key, RunID: actor.RunID, AgentID: actor.AgentID, Turn: actor.Turn, Number: number, Fallback: model.ModelID != preferred}
		out, retry, err := g.callModel(ctx, model, in, permit, attempt, deadline)
		if err == nil {
			return out, nil
		}
		if !retry {
			return RoutedResult{}, err
		}
		last = err
	}
	return RoutedResult{}, last
}

func (g Gateway) callModel(ctx context.Context, model Candidate, in Request, permit Permit, attempt Attempt, deadline time.Time) (RoutedResult, bool, error) {
	attempt.Started = time.Now()
	call, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var visible, timedOut, returned bool
	var timer *time.Timer
	if !deadline.IsZero() {
		timer = time.AfterFunc(time.Until(deadline), func() {
			mu.Lock()
			defer mu.Unlock()
			if !visible && !returned {
				timedOut = true
				cancel()
			}
		})
	}
	if downstream := in.EmitText; downstream != nil {
		in.EmitText = func(chunk string) {
			mu.Lock()
			allowed := !timedOut && call.Err() == nil && chunk != ""
			if allowed {
				visible = true
				if timer != nil {
					timer.Stop()
				}
			}
			mu.Unlock()
			if allowed {
				downstream(chunk)
			}
		}
	}
	result, callErr := g.Provider.Call(call, model, in)
	mu.Lock()
	returned = true
	retryAllowed, expired := !visible, timedOut
	mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	cancel()
	done, finish := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finish()
	attempt.Finished = time.Now()
	out := RoutedResult{Result: result, ModelID: model.ModelID, ModelName: model.Name, PriceID: model.PriceVersion, Fallback: attempt.Fallback}
	var providerErr ProviderError
	var settleErr error
	if callErr == nil {
		attempt.Status = "succeeded"
		attempt.Usage = result.Usage
		if result.Usage != nil {
			cost, err := g.Budget.Settle(done, attempt.OperationKey, *result.Usage)
			settleErr = err
			if err == nil {
				out.Cost = &cost
			}
		}
	} else {
		attempt.Status = "failed"
		if errors.As(callErr, &providerErr) {
			attempt.ProviderCode = providerErr.Status
			attempt.Unsent = providerErr.Unsent
			attempt.TransientFailure = !providerErr.Unsent && providerErr.Retryable && ctx.Err() == nil
		}
		if expired && ctx.Err() == nil {
			attempt.TransientFailure = true
		}
		if ctx.Err() != nil {
			attempt.Status = "cancelled"
		}
		if attempt.Unsent || providerRejected(attempt.ProviderCode) {
			attempt.Status = "rejected"
			settleErr = g.Budget.Release(done, attempt.OperationKey)
		}
	}
	if err := g.Resilience.Complete(done, permit, attempt); err != nil || settleErr != nil {
		return RoutedResult{}, false, errors.Join(callErr, settleErr, err)
	}
	retry := retryAllowed && ctx.Err() == nil && (attempt.Unsent || attempt.TransientFailure)
	return out, retry, callErr
}

func providerRejected(code int) bool {
	return code >= 300 && code < 500 && code != http.StatusRequestTimeout
}

func operationKey(operation, model string) string {
	sum := sha256.Sum256([]byte(operation + "\x00" + model))
	return hex.EncodeToString(sum[:])
}

func modelOrder(models []Candidate, key string) []Candidate {
	models = slices.Clone(models)
	slices.SortStableFunc(models, func(a, b Candidate) int { return a.Priority - b.Priority })
	seed := sha256.Sum256([]byte(key))
	random := rand.New(rand.NewPCG(binary.BigEndian.Uint64(seed[:8]), binary.BigEndian.Uint64(seed[8:16])))
	for first := 0; first < len(models); {
		end := first + 1
		for end < len(models) && models[end].Priority == models[first].Priority {
			end++
		}
		for i := first; i+1 < end; i++ {
			var weights int64
			for _, candidate := range models[i:end] {
				weights += int64(max(candidate.Weight, 1))
			}
			pick := random.Int64N(weights)
			for chosen := i; chosen < end; chosen++ {
				pick -= int64(max(models[chosen].Weight, 1))
				if pick < 0 {
					models[i], models[chosen] = models[chosen], models[i]
					break
				}
			}
		}
		first = end
	}
	return models
}

func (in Request) valid() bool {
	if !in.Tier.Valid() || in.MaxTokens < 0 {
		return false
	}
	switch in.Tier {
	case Image:
		return strings.TrimSpace(in.ImagePrompt) != ""
	case Embedding:
		return len(in.Embeddings) > 0 && len(in.Embeddings) <= 32
	default:
		return len(in.Messages) > 0
	}
}

func (in Request) supports(model Candidate) bool {
	var capability string
	switch in.Tier {
	case Default, Fast:
		capability = "text"
	default:
		capability = string(in.Tier)
	}
	if in.Tier != Embedding && model.Price.OutputPerMillion > 0 && outputLimit(model, in.MaxTokens) == 0 {
		return false
	}
	return slices.Contains(model.Capabilities, capability) && (len(in.Tools) == 0 || slices.Contains(model.Capabilities, "tool_call")) &&
		(len(in.Pictures) == 0 || slices.Contains(model.Capabilities, "vision"))
}

func (in Request) estimate(model Candidate) Usage {
	var estimate Usage
	for _, message := range in.Messages {
		estimate.PromptTokens += int64(len(message.Content) + len(message.ReasoningContent) + 8)
		for _, call := range message.ToolCalls {
			estimate.PromptTokens += int64(len(call.Function.Arguments) + len(call.Function.Name) + len(call.ID) + 8)
		}
	}
	for _, tool := range in.Tools {
		estimate.PromptTokens += int64(len(tool.Function.Name) + len(tool.Function.Description) + len(tool.Function.Parameters) + 8)
	}
	for _, value := range in.Embeddings {
		estimate.PromptTokens += int64(len(value) + 4)
	}
	estimate.PromptTokens += int64(len(in.Pictures)) * 4096
	if in.Tier != Embedding {
		estimate.CompletionTokens = int64(outputLimit(model, in.MaxTokens))
	}
	if in.Tier == Image {
		estimate.Images = 1
		estimate.PromptTokens += int64(len(in.ImagePrompt))
	}
	return estimate
}
