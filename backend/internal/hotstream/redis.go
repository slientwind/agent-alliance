package hotstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Vault interface {
	Seal(string, string) ([]byte, error)
	Open([]byte, string) (string, error)
}

type Redis struct {
	client *redis.Client
	vault  Vault
}

func OpenRedis(address string, vault Vault) (*Redis, error) {
	options, err := redis.ParseURL(address)
	if err != nil || vault == nil {
		return nil, errors.New("invalid HOT_STREAM_REDIS_URL or stream vault")
	}
	return &Redis{client: redis.NewClient(options), vault: vault}, nil
}

func (bus *Redis) Close() error { return bus.client.Close() }

func streamKey(project string) string { return "alliance:hot:project:" + project }

func (bus *Redis) Publish(ctx context.Context, project string, event Event) error {
	if err := validate(project, event); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	key := streamKey(project)
	sealed, err := bus.vault.Seal(string(encoded), key)
	if err != nil {
		return err
	}
	_, err = bus.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: maxRecords, Values: map[string]any{"sealed": base64.RawStdEncoding.EncodeToString(sealed)}})
		p.Expire(ctx, key, retention)
		return nil
	})
	return err
}

func (bus *Redis) Read(ctx context.Context, project, after string) ([]Record, error) {
	if !validID(project) {
		return nil, ErrEvent
	}
	if after == "" {
		after = "0-0"
	}
	if _, valid := timestamp(after); !valid {
		return nil, ErrEvent
	}
	key := streamKey(project)
	streams, err := bus.client.XRead(ctx, &redis.XReadArgs{Streams: []string{key, after}, Block: maxWait, Count: 32}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			at, valid := timestamp(msg.ID)
			value, stringValue := msg.Values["sealed"].(string)
			if !valid || !stringValue || len(value) > 48<<10 {
				return nil, ErrEvent
			}
			if time.Since(at) >= retention {
				records = append(records, Record{ID: msg.ID, At: at})
				continue
			}
			sealed, err := base64.RawStdEncoding.DecodeString(value)
			if err != nil {
				return nil, ErrEvent
			}
			var event Event
			raw, err := bus.vault.Open(sealed, key)
			if err != nil || json.Unmarshal([]byte(raw), &event) != nil || validate(project, event) != nil {
				return nil, ErrEvent
			}
			records = append(records, Record{msg.ID, at, event})
		}
	}
	return records, nil
}

func timestamp(id string) (time.Time, bool) {
	ms, sequence, ok := strings.Cut(id, "-")
	if !ok {
		return time.Time{}, false
	}
	value, err := strconv.ParseInt(ms, 10, 64)
	_, seqErr := strconv.ParseUint(sequence, 10, 64)
	return time.UnixMilli(value), err == nil && seqErr == nil && value >= 0
}
