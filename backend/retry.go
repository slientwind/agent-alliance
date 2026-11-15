package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

type upstreamHTTPError struct{ Status int }

func (e upstreamHTTPError) Error() string { return fmt.Sprintf("远端返回 HTTP %d", e.Status) }
func transientFailure(err error) bool {
	var h upstreamHTTPError
	if errors.As(err, &h) {
		return h.Status == 429 || h.Status == 502 || h.Status == 503 || h.Status == 504
	}
	var n net.Error
	return errors.As(err, &n) && n.Timeout()
}
func retryDelay(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(time.Duration(1<<attempt) * 250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type upstreamNetworkError struct{ timedOut bool }

func (e upstreamNetworkError) Error() string {
	return "远端连接失败（检查地址与网络策略）"
}
func (e upstreamNetworkError) Timeout() bool   { return e.timedOut }
func (e upstreamNetworkError) Temporary() bool { return e.timedOut }
