package hotstream

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const presenceTTL = 35 * time.Second

type presenceLease struct {
	member  string
	expires time.Time
}

func (bus *Memory) KeepPresence(ctx context.Context, project, member, session string) error {
	if err := validPresence(ctx, project, member, session); err != nil {
		return err
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.presence[project] == nil {
		bus.presence[project] = make(map[string]presenceLease)
	}
	bus.presence[project][session] = presenceLease{member, time.Now().Add(presenceTTL)}
	return nil
}

func (bus *Memory) RemovePresence(ctx context.Context, project, member, session string) error {
	if err := validPresence(ctx, project, member, session); err != nil {
		return err
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	entries := bus.presence[project]
	if entries[session].member == member {
		delete(entries, session)
	}
	if len(entries) == 0 {
		delete(bus.presence, project)
	}
	return nil
}

func (bus *Memory) OnlineMembers(ctx context.Context, project string) ([]string, error) {
	if !validID(project) {
		return nil, ErrEvent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	bus.mu.Lock()
	defer bus.mu.Unlock()
	entries := bus.presence[project]
	var members []string
	for session, lease := range entries {
		if lease.expires.After(now) {
			members = append(members, lease.member)
		} else {
			delete(entries, session)
		}
	}
	if len(entries) == 0 {
		delete(bus.presence, project)
	}
	slices.Sort(members)
	return slices.Compact(members), nil
}

func presenceKey(project string) string { return streamKey(project) + ":presence" }

func (bus *Redis) KeepPresence(ctx context.Context, project, member, session string) error {
	if err := validPresence(ctx, project, member, session); err != nil {
		return err
	}
	now, err := bus.client.Time(ctx).Result()
	if err != nil {
		return err
	}
	key := presenceKey(project)
	_, err = bus.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.UnixMilli(), 10))
		p.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(presenceTTL).UnixMilli()), Member: member + "/" + session})
		p.Expire(ctx, key, 2*presenceTTL)
		return nil
	})
	return err
}

func (bus *Redis) RemovePresence(ctx context.Context, project, member, session string) error {
	if err := validPresence(ctx, project, member, session); err != nil {
		return err
	}
	return bus.client.ZRem(ctx, presenceKey(project), member+"/"+session).Err()
}

func (bus *Redis) OnlineMembers(ctx context.Context, project string) ([]string, error) {
	if !validID(project) {
		return nil, ErrEvent
	}
	now, err := bus.client.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	key := presenceKey(project)
	leases, err := bus.client.ZRangeByScore(ctx, key, &redis.ZRangeBy{
		Min: "(" + strconv.FormatInt(now.UnixMilli(), 10), Max: "+inf",
	}).Result()
	if err != nil {
		return nil, err
	}
	members := make([]string, 0, len(leases))
	for _, lease := range leases {
		member, session, ok := strings.Cut(lease, "/")
		if ok && validID(member) && validID(session) {
			members = append(members, member)
		}
	}
	slices.Sort(members)
	return slices.Compact(members), nil
}

func validPresence(ctx context.Context, project, member, session string) error {
	if !validID(project) || !validID(member) || !validID(session) {
		return ErrEvent
	}
	return ctx.Err()
}
