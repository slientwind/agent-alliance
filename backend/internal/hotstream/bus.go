// Package hotstream carries short-lived project events. Durable project state
// and authorization remain in PostgreSQL.
package hotstream

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	Draft   = "draft.delta"
	Thought = "thinking.progress"
	Comment = "comments.changed"
	Join    = "presence.joined"
	Leave   = "presence.left"

	maxRecords = 256
	maxTopics  = 192
	maxText    = 4096
	retention  = 10 * time.Minute
	maxWait    = time.Second
)

var ErrEvent = errors.New("invalid live project event")

type Event struct {
	Kind    string `json:"kind"`
	Run     string `json:"run,omitempty"`
	Step    int    `json:"step,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Control int    `json:"control,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Text    string `json:"text,omitempty"`
	Member  string `json:"member,omitempty"`
	Session string `json:"session,omitempty"`
}

type Record struct {
	ID string
	At time.Time
	Event
}

type Publisher interface {
	Publish(context.Context, string, Event) error
}

type HotStreamBus interface {
	Publisher
	Read(context.Context, string, string) ([]Record, error)
	KeepPresence(context.Context, string, string, string) error
	RemovePresence(context.Context, string, string, string) error
	OnlineMembers(context.Context, string) ([]string, error)
}

func validate(project string, event Event) error {
	if !validID(project) || len(event.Text) > maxText || !utf8.ValidString(event.Text) || strings.ContainsRune(event.Text, 0) {
		return ErrEvent
	}
	switch event.Kind {
	case Draft:
		if validID(event.Run) && event.Step > 0 && event.Attempt > 0 && event.Control >= 0 && event.Offset >= 0 && event.Text != "" {
			return nil
		}
	case Thought:
		if validID(event.Run) && event.Step > 0 && event.Control >= 0 {
			return nil
		}
	case Comment:
		return nil
	case Join, Leave:
		if validID(event.Member) && validID(event.Session) {
			return nil
		}
	}
	return ErrEvent
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
