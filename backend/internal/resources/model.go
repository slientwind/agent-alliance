package resources

import (
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Vault struct{ aead cipher.AEAD }

func NewVault(key string) (Vault, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return Vault{}, errors.New("RESOURCE_KEY must be 32 hex-encoded bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return Vault{}, err
	}
	aead, err := cipher.NewGCM(block)
	return Vault{aead}, err
}
func (v Vault) Seal(value, scope string) ([]byte, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return v.aead.Seal(n, n, []byte(value), []byte(scope)), nil
}
func (v Vault) Open(value []byte, scope string) (string, error) {
	n := v.aead.NonceSize()
	if len(value) < n {
		return "", errors.New("invalid secret reference")
	}
	raw, err := v.aead.Open(nil, value[:n], value[n:], []byte(scope))
	return string(raw), err
}
func Endpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Reply struct {
	Receipt *RemoteWait
	File    *File
	Text    string
	Tokens  int
}
type Model struct {
	ProjectGrant    string
	OutputOperation string
	ResourceID      string
	KeyGeneration   int

	SourceRevision                                 int
	Policy                                         RemotePolicy
	Tool                                           *Tool
	Sources                                        []Source
	Pictures                                       []knowledge.Picture
	ImageRegion                                    *knowledge.ImageRegion
	ToolInput                                      string
	Endpoint, Name, Key, Operation, Team, BudgetID string
	Estimate                                       *string
	External                                       bool
}

func (m Model) Generate(ctx context.Context, messages []Message) (Reply, error) {
	if m.Tool != nil {
		text := m.ToolInput
		if text == "" && len(m.Sources) == 0 {
			for _, msg := range messages {
				if msg.Role == "user" {
					text = msg.Content
					break
				}
			}
		}
		return m.Tool.invoke(ctx, text, m.Sources)
	}
	if m.External {
		var parts []map[string]string
		for _, msg := range messages {
			if msg.Role == "user" {
				parts = append(parts, map[string]string{"kind": "text", "text": msg.Content})
			}
		}
		id := m.Operation
		if id == "" {
			id = web.Token()
		}
		sub, stop := context.WithTimeout(ctx, time.Duration(m.Policy.ExecutionSeconds)*time.Second)
		defer stop()
		return m.remote(sub, "message/send", nil, map[string]any{"message": map[string]any{"kind": "message", "messageId": id, "role": "user", "parts": parts}, "configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}}})
	}
	if !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("invalid provider endpoint")
	}
	if ProducesPNG(m.OutputOperation) {
		return m.generateImage(ctx, messages)
	}
	var providerMessages any = messages
	if m.OutputOperation == VisionOperation {
		var err error
		providerMessages, err = visionMessages(messages, m.Pictures)
		if err != nil {
			return Reply{}, err
		}
	}
	body, err := json.Marshal(map[string]any{"model": m.Name, "messages": providerMessages, "max_tokens": 1800})
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Key)
	client, err := providerClient()
	if err != nil {
		return Reply{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Reply{}, errors.New("模型请求结果待核对")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Reply{}, ProviderStatus(resp.StatusCode)
	}
	var result struct {
		Choices []struct{ Message Message }
		Usage   struct {
			Total int `json:"total_tokens"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil || len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return Reply{}, errors.New("供应商响应缺少有效正文")
	}
	return Reply{Text: result.Choices[0].Message.Content, Tokens: result.Usage.Total}, nil
}
