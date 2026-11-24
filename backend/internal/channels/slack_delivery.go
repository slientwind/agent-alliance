package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s Slack) Serve(ctx context.Context) {
	if s.configured() {
		s.serve(ctx, s.postReply, slackFormat)
	}
}

var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackFormat(text, link string) string {
	if link == "" {
		return slackEscape.Replace(text)
	}
	return slackEscape.Replace(text) + "\n<" + slackEscape.Replace(link) + "|打开企业网页中的原任务>"
}

func (s Slack) postReply(ctx context.Context, job imJob, text string) (string, time.Duration) {
	payload := struct {
		Channel     string `json:"channel"`
		Thread      string `json:"thread_ts"`
		Text        string `json:"text"`
		Parse       string `json:"parse"`
		LinkNames   bool   `json:"link_names"`
		UnfurlLinks bool   `json:"unfurl_links"`
		UnfurlMedia bool   `json:"unfurl_media"`
	}{Channel: job.chat, Thread: job.thread, Text: text, Parse: "none"}
	data, err := json.Marshal(payload)
	if err != nil {
		return "failed", 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		return "failed", 0
	}
	req.Header.Set("Authorization", "Bearer "+s.bot)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := s.client.Do(req)
	if err != nil {
		return "unknown", 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return imRateLimited(res.Header.Get("Retry-After"), job.attempts)
	}
	if res.StatusCode >= 500 {
		return "unknown", 0
	}
	if res.StatusCode != http.StatusOK {
		return "failed", 0
	}
	var response struct {
		OK      *bool  `json:"ok"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&response) != nil || response.OK == nil {
		return "unknown", 0
	}
	if !*response.OK {
		return "failed", 0
	}
	if response.Channel != job.chat || !slackTimestamp.MatchString(response.TS) {
		return "unknown", 0
	}
	return "sent", 0
}
