package knowledgesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	feishuAPI     = "https://open.feishu.cn/open-apis"
	confluenceAPI = "/wiki/api/v2"
	graphAPI      = "https://graph.microsoft.com/v1.0"
	dingtalkAPI   = "https://api.dingtalk.com/v1.0"
	wecomAPI      = "https://qyapi.weixin.qq.com/cgi-bin"
)

type httpDirectory struct {
	client providerClient
	config collectionView
	secret collectionSecret
	token  string
}

func (p providerClient) openDirectory(ctx context.Context, config collectionView, secret collectionSecret) (remoteDirectory, error) {
	d := &httpDirectory{client: p, config: config, secret: secret}
	var err error
	switch config.Provider {
	case "feishu_drive", "feishu_wiki":
		d.token, err = d.feishuToken(ctx)
	case "dingtalk":
		d.token, err = d.dingtalkToken(ctx)
	case "wecom":
		d.token, err = d.wecomToken(ctx)
	case "sharepoint":
		d.token, err = d.graphToken(ctx)
	case "confluence", "s3":
	default:
		err = errInvalid
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (d *httpDirectory) List(ctx context.Context, folder, cursor string) ([]directoryEntry, string, error) {
	switch d.config.Provider {
	case "feishu_wiki":
		return d.listFeishuWiki(ctx, folder, cursor)
	case "feishu_drive":
		return d.listFeishuDrive(ctx, folder, cursor)
	case "confluence":
		return d.listConfluence(ctx, cursor)
	case "sharepoint":
		return d.listGraph(ctx, folder, cursor)
	case "dingtalk":
		return d.listDingtalk(ctx, folder, cursor)
	case "wecom":
		return d.listWecom(ctx, folder, cursor)
	case "s3":
		return d.listS3(ctx, folder, cursor)
	}
	return nil, "", errInvalid
}

func (d *httpDirectory) Open(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	switch d.config.Provider {
	case "feishu_wiki", "feishu_drive":
		return d.openFeishu(ctx, item)
	case "confluence":
		return d.openConfluence(ctx, item)
	case "sharepoint":
		return d.openGraph(ctx, item)
	case "dingtalk":
		return d.openDingtalk(ctx, item)
	case "wecom":
		return d.openWecom(ctx, item)
	case "s3":
		return d.openS3(ctx, item)
	}
	return nil, errInvalid
}

func directoryMedia(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".log":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	return ""
}

func withQuery(target string, vals url.Values) string {
	if len(vals) == 0 {
		return target
	}
	return target + "?" + vals.Encode()
}

func directoryJSONRequest(ctx context.Context, method, target string, body any) (*http.Request, error) {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, errInvalid
		}
		input = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, target, input)
	if err != nil {
		return nil, errInvalid
	}
	if input != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

func (d *httpDirectory) request(ctx context.Context, r *http.Request) (*http.Response, error) {
	client, err := d.client.client(ctx, r.URL.String())
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(r)
	if err != nil {
		return nil, errUnavailable
	}
	return resp, nil
}

func directoryHTTPStatus(status int) error {
	switch status {
	case 200:
		return nil
	case 401, 403, 404, 410:
		return errDenied
	case 400:
		return errInvalid
	default:
		return errUnavailable
	}
}

func (d *httpDirectory) readJSON(ctx context.Context, r *http.Request, out any, limit int64) error {
	r.Header.Set("Accept", "application/json")
	resp, err := d.request(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := directoryHTTPStatus(resp.StatusCode); err != nil {
		return err
	}
	body := &io.LimitedReader{R: resp.Body, N: limit + 1}
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(out); err != nil {
		return errInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || body.N == 0 {
		return errInvalid
	}
	return nil
}

func (d *httpDirectory) readFile(ctx context.Context, req *http.Request, allowRedirect bool) (io.ReadCloser, error) {
	resp, err := d.request(ctx, req)
	if err != nil {
		return nil, err
	}
	if allowRedirect && (resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusTemporaryRedirect) {
		defer resp.Body.Close()
		loc, err := resp.Location()
		if err != nil || loc.Host == "" || strings.EqualFold(loc.Host, req.URL.Host) {
			return nil, errInvalid
		}
		return d.downloadURL(ctx, loc.String(), nil)
	}
	if err := directoryHTTPStatus(resp.StatusCode); err != nil {
		resp.Body.Close()
		return nil, err
	}
	if resp.ContentLength > maxKnowledgeFile {
		resp.Body.Close()
		return nil, errInvalid
	}
	return resp.Body, nil
}

func (d *httpDirectory) downloadURL(ctx context.Context, raw string, headers http.Header) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errInvalid
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errInvalid
	}
	for k, v := range headers {
		r.Header[k] = v
	}
	return d.readFile(ctx, r, false)
}

func bearer(r *http.Request, token string) {
	r.Header.Set("Authorization", "Bearer "+token)
}

func nextFromLink(raw string, base *url.URL, param string) (string, error) {
	if raw == "" {
		return "", nil
	}
	link, err := base.Parse(raw)
	if err != nil || link.Scheme != base.Scheme || link.Host != base.Host || link.User != nil || link.Path != base.Path || link.Fragment != "" {
		return "", errInvalid
	}
	values := link.Query()[param]
	if len(values) != 1 || !validRemoteID(values[0], 4096) {
		return "", errInvalid
	}
	return values[0], nil
}
