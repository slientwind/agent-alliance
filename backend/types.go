package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	Key     string `json:"apiKey,omitempty"`
	HasKey  bool   `json:"hasKey"`
}
type Card struct {
	Name               string                     `json:"name"`
	Description        string                     `json:"description"`
	URL                string                     `json:"url"`
	Version            string                     `json:"version"`
	Protocol           string                     `json:"protocol"`
	ProtocolVersion    string                     `json:"protocolVersion,omitempty"`
	Skills             []Skill                    `json:"skills"`
	PreferredTransport string                     `json:"preferredTransport,omitempty"`
	SecuritySchemes    map[string]map[string]any  `json:"securitySchemes,omitempty"`
	Security           []map[string][]string      `json:"security,omitempty"`
	Extra              map[string]json.RawMessage `json:"-"`
}
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}
type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Version     int       `json:"version"`
	Prompt      string    `json:"prompt"`
	ProviderID  string    `json:"providerId"`
	Card        *Card     `json:"card,omitempty"`
	Key         string    `json:"apiKey,omitempty"`
	HasKey      bool      `json:"hasKey"`
	ClearKey    bool      `json:"clearApiKey,omitempty"`
	RetrySafe   bool      `json:"retrySafe"`
	CreatedAt   time.Time `json:"createdAt"`
}
type Node struct {
	ID          string   `json:"id"`
	AgentID     string   `json:"agentId"`
	DependsOn   []string `json:"dependsOn"`
	Instruction string   `json:"instruction"`
}
type Release struct {
	Version     int              `json:"version"`
	Nodes       []Node           `json:"nodes"`
	Agents      map[string]Agent `json:"agents"`
	PublishedAt time.Time        `json:"publishedAt"`
}
type Service struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Nodes          []Node   `json:"nodes"`
	Version        int      `json:"version"`
	ReleaseVersion int      `json:"releaseVersion"`
	Published      *Release `json:"published,omitempty"`
}
type Span struct {
	RetryCount   int         `json:"retryCount"`
	Remote       *RemoteTask `json:"remoteTask,omitempty"`
	Model        string      `json:"model,omitempty"`
	ID           string      `json:"id"`
	ParentID     string      `json:"parentId"`
	NodeID       string      `json:"nodeId"`
	Name         string      `json:"name"`
	AgentVersion int         `json:"agentVersion"`
	Status       string      `json:"status"`
	Input        string      `json:"input"`
	Output       string      `json:"output"`
	Error        string      `json:"error,omitempty"`
	StartedAt    time.Time   `json:"startedAt"`
	EndedAt      *time.Time  `json:"endedAt,omitempty"`
	DurationMS   int64       `json:"durationMs"`
	Tokens       *int        `json:"tokens"`
	Mock         bool        `json:"mock"`
}
type RunEvent struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Message  string    `json:"message"`
	WorkerID string    `json:"workerId,omitempty"`
	Attempt  int       `json:"attempt"`
}
type Run struct {
	A2A        *A2AInfo   `json:"a2a,omitempty"`
	ActorID    string     `json:"actorId,omitempty"`
	ID         string     `json:"id"`
	TraceID    string     `json:"traceId"`
	TargetType string     `json:"targetType"`
	TargetID   string     `json:"targetId"`
	TargetName string     `json:"targetName"`
	Attempt    int        `json:"attempt"`
	WorkerID   string     `json:"workerId,omitempty"`
	Deadline   *time.Time `json:"deadline,omitempty"`
	Events     []RunEvent `json:"events"`
	Version    int        `json:"version"`
	Status     string     `json:"status"`
	Input      string     `json:"input"`
	Output     string     `json:"output"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	DurationMS int64      `json:"durationMs"`
	Spans      []Span     `json:"spans"`
}
type State struct {
	Providers map[string]Provider `json:"providers"`
	Agents    map[string]Agent    `json:"agents"`
	Services  map[string]Service  `json:"services"`
	Runs      map[string]Run      `json:"runs"`
	Secrets   map[string]string   `json:"secrets"`
}

func newState() State {
	return State{map[string]Provider{}, map[string]Agent{}, map[string]Service{}, map[string]Run{}, map[string]string{}}
}
func id(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func now() time.Time         { return time.Now().UTC() }
func terminal(s string) bool { return s == "succeeded" || s == "failed" || s == "cancelled" }

// Preserve standard capability/provider/mode fields and future non-executable metadata.
func (c *Card) UnmarshalJSON(b []byte) error {
	type plain Card
	var v plain
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	*c = Card(v)
	if e := json.Unmarshal(b, &c.Extra); e != nil {
		return e
	}
	for _, k := range []string{"name", "description", "url", "version", "protocol", "protocolVersion", "skills", "preferredTransport", "securitySchemes", "security"} {
		delete(c.Extra, k)
	}
	return nil
}
func (c Card) MarshalJSON() ([]byte, error) {
	type plain Card
	b, e := json.Marshal(plain(c))
	if e != nil {
		return nil, e
	}
	m := map[string]json.RawMessage{}
	for k, v := range c.Extra {
		m[k] = v
	}
	var known map[string]json.RawMessage
	if e = json.Unmarshal(b, &known); e != nil {
		return nil, e
	}
	for k, v := range known {
		m[k] = v
	}
	return json.Marshal(m)
}
