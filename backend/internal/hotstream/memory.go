package hotstream

import (
	"context"
	"strconv"
	"sync"
	"time"
)

type Memory struct {
	mu      sync.Mutex
	topics  map[string]*topic
	seq     uint64
	created chan struct{}
}

type topic struct {
	records []Record
	wake    chan struct{}
	seen    time.Time
}

func NewMemory() *Memory {
	return &Memory{topics: make(map[string]*topic), created: make(chan struct{})}
}

func (bus *Memory) Publish(ctx context.Context, project string, event Event) error {
	if err := validate(project, event); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	now := time.Now()
	stream := bus.topics[project]
	if stream == nil {
		if len(bus.topics) == maxTopics {
			var oldest string
			for id, candidate := range bus.topics {
				if oldest == "" || candidate.seen.Before(bus.topics[oldest].seen) {
					oldest = id
				}
			}
			close(bus.topics[oldest].wake)
			delete(bus.topics, oldest)
		}
		stream = &topic{wake: make(chan struct{})}
		bus.topics[project] = stream
		close(bus.created)
		bus.created = make(chan struct{})
	}
	if len(stream.records) == maxRecords {
		copy(stream.records, stream.records[1:])
		stream.records = stream.records[:maxRecords-1]
	}
	bus.seq++
	stream.records = append(stream.records, Record{strconv.FormatUint(bus.seq, 10), now, event})
	stream.seen = now
	close(stream.wake)
	stream.wake = make(chan struct{})
	return nil
}

func (bus *Memory) Read(ctx context.Context, project, after string) ([]Record, error) {
	if !validID(project) {
		return nil, ErrEvent
	}
	var cursor uint64
	if after != "" {
		var err error
		cursor, err = strconv.ParseUint(after, 10, 64)
		if err != nil {
			return nil, ErrEvent
		}
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	for {
		bus.mu.Lock()
		wake := bus.created
		var batch []Record
		if stream := bus.topics[project]; stream != nil {
			wake = stream.wake
			for _, record := range stream.records {
				seq, _ := strconv.ParseUint(record.ID, 10, 64)
				if seq > cursor && time.Since(record.At) < retention {
					batch = append(batch, record)
				}
				if len(batch) == 32 {
					break
				}
			}
		}
		bus.mu.Unlock()
		if len(batch) != 0 {
			return batch, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-wake:
		}
	}
}
