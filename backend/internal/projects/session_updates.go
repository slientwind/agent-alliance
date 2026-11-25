package projects

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionUpdates fans out commit notifications on one PostgreSQL connection
// per API process. The journal remains the source of replay after a reconnect.
type SessionUpdates struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func StartSessionUpdates(ctx context.Context, db *pgxpool.Pool) (*SessionUpdates, error) {
	config := db.Config().ConnConfig
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["application_name"] = "alliance-session-updates"
	conn, err := listenForSessionUpdates(ctx, config)
	if err != nil {
		return nil, err
	}
	updates := &SessionUpdates{subscribers: make(map[string]map[chan struct{}]struct{})}
	go updates.receive(ctx, config, conn)
	return updates, nil
}

func listenForSessionUpdates(ctx context.Context, config *pgx.ConnConfig) (*pgx.Conn, error) {
	connect, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	conn, err := pgx.ConnectConfig(connect, config)
	if err != nil {
		return nil, err
	}
	if _, err = conn.Exec(connect, "listen alliance_project_updates"); err != nil {
		closeSessionListener(conn)
		return nil, err
	}
	return conn, nil
}

func closeSessionListener(conn *pgx.Conn) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = conn.Close(ctx)
}

func (updates *SessionUpdates) receive(ctx context.Context, config *pgx.ConnConfig, conn *pgx.Conn) {
	for {
		for ctx.Err() == nil {
			message, err := conn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			if message.Payload != "" {
				updates.signal(message.Payload)
			}
		}
		closeSessionListener(conn)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("project session notifications disconnected")
		timer := time.NewTicker(time.Second)
		for conn = nil; conn == nil; {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				conn, _ = listenForSessionUpdates(ctx, config)
			}
		}
		timer.Stop()
		updates.signal("")
	}
}

func (updates *SessionUpdates) subscribe(project string) (<-chan struct{}, func()) {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	ch := make(chan struct{}, 1)
	watchers := updates.subscribers[project]
	if watchers == nil {
		watchers = make(map[chan struct{}]struct{})
		updates.subscribers[project] = watchers
	}
	watchers[ch] = struct{}{}
	return ch, sync.OnceFunc(func() {
		updates.mu.Lock()
		defer updates.mu.Unlock()
		delete(watchers, ch)
		if len(watchers) == 0 {
			delete(updates.subscribers, project)
		}
	})
}

func (updates *SessionUpdates) signal(project string) {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	if project != "" {
		signalWatchers(updates.subscribers[project])
		return
	}
	for _, watchers := range updates.subscribers {
		signalWatchers(watchers)
	}
}

func signalWatchers(watchers map[chan struct{}]struct{}) {
	for ch := range watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
