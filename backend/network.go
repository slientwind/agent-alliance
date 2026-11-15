package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func validateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("必须使用不含账号信息的 HTTP(S) URL")
	}
	return nil
}
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
func httpClient(allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("远端 DNS 解析失败")
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("远端无可用地址")
		}
		for _, ip := range ips {
			if !allowPrivate && blockedIP(ip.IP) {
				return nil, fmt.Errorf("禁止访问私网或本地地址")
			}
		}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return fmt.Errorf("不允许远端重定向") }}
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func nonempty(s string) bool { return strings.TrimSpace(s) != "" }
