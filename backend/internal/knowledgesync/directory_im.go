package knowledgesync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (d *httpDirectory) dingtalkToken(ctx context.Context) (string, error) {
	r, err := directoryJSONRequest(ctx, http.MethodPost, dingtalkAPI+"/oauth2/accessToken", map[string]string{
		"appKey": d.config.Settings.Account, "appSecret": d.secret.Token,
	})
	if err != nil {
		return "", err
	}
	var out struct{ AccessToken string }
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return "", err
	}
	if !validRemoteID(out.AccessToken, 4096) {
		return "", errDenied
	}
	return out.AccessToken, nil
}

func (d *httpDirectory) dingDirectoryPath() string {
	return dingtalkAPI + "/storage/spaces/" + url.PathEscape(d.config.Settings.Container) + "/dentries"
}

func (d *httpDirectory) listDingtalk(ctx context.Context, folder, cursor string) ([]directoryEntry, string, error) {
	query := url.Values{"parentId": {folder}, "unionId": {d.config.Settings.Operator}, "maxResults": {"50"}}
	if cursor != "" {
		query.Set("nextToken", cursor)
	}
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(d.dingDirectoryPath(), query), nil)
	if err != nil {
		return nil, "", err
	}
	r.Header.Set("x-acs-dingtalk-access-token", d.token)
	var out struct {
		NextToken string
		Dentries  []struct {
			ID, Name, Type, Status, ModifiedTime, Extension string
			Version                                         int64
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, "", err
	}
	list := make([]directoryEntry, 0, len(out.Dentries))
	for _, node := range out.Dentries {
		if !validRemoteToken(node.ID) || (node.Status != "" && !strings.EqualFold(node.Status, "NORMAL")) {
			continue
		}
		item := directoryEntry{ID: node.ID, Title: node.Name, Revision: node.ModifiedTime}
		if node.Version > 0 {
			item.Revision = "v:" + strconv.FormatInt(node.Version, 10)
		}
		switch strings.ToUpper(node.Type) {
		case "FOLDER":
			item.FolderID = node.ID
		case "FILE":
			item.Media = directoryMedia(node.Name)
			if item.Media == "" && node.Extension != "" {
				item.Media = directoryMedia("file." + strings.TrimPrefix(node.Extension, "."))
			}
		}
		list = append(list, item)
	}
	return list, out.NextToken, nil
}

func (d *httpDirectory) openDingtalk(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	option := map[string]any{"preferIntranet": false}
	if version, ok := strings.CutPrefix(item.Revision, "v:"); ok {
		n, err := strconv.ParseInt(version, 10, 64)
		if err != nil || n <= 0 {
			return nil, errInvalid
		}
		option["version"] = n
	}
	query := url.Values{"unionId": {d.config.Settings.Operator}}
	r, err := directoryJSONRequest(ctx, http.MethodPost,
		withQuery(d.dingDirectoryPath()+"/"+url.PathEscape(item.ID)+"/downloadInfos/query", query), map[string]any{"option": option})
	if err != nil {
		return nil, err
	}
	r.Header.Set("x-acs-dingtalk-access-token", d.token)
	var out struct {
		HeaderSignatureInfo struct {
			Headers      map[string]string
			ResourceURLs []string
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, err
	}
	if len(out.HeaderSignatureInfo.ResourceURLs) == 0 || len(out.HeaderSignatureInfo.Headers) > 20 {
		return nil, errInvalid
	}
	target := out.HeaderSignatureInfo.ResourceURLs[0]
	parsed, err := url.Parse(target)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".aliyuncs.com") {
		return nil, errEgress
	}
	headers := make(http.Header)
	for key, value := range out.HeaderSignatureInfo.Headers {
		name := strings.ToLower(key)
		if validRemoteID(value, 4096) && (name == "authorization" || name == "date" || strings.HasPrefix(name, "x-oss-")) {
			headers.Set(key, value)
		}
	}
	return d.downloadURL(ctx, target, headers)
}

func (d *httpDirectory) wecomToken(ctx context.Context) (string, error) {
	query := url.Values{"corpid": {d.config.Settings.Account}, "corpsecret": {d.secret.Token}}
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(wecomAPI+"/gettoken", query), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Code  *int   `json:"errcode"`
		Token string `json:"access_token"`
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return "", err
	}
	if err := wecomStatus(out.Code); err != nil {
		return "", err
	}
	if !validRemoteID(out.Token, 4096) {
		return "", errDenied
	}
	return out.Token, nil
}

func wecomStatus(code *int) error {
	if code == nil {
		return errInvalid
	}
	switch *code {
	case 0:
		return nil
	case 40001, 40014, 42001, 48002, 60011, 301002, 640005:
		return errDenied
	default:
		return errUnavailable
	}
}

func (d *httpDirectory) wecomDrive(ctx context.Context, action string, input any, out any) error {
	query := url.Values{"access_token": {d.token}}
	r, err := directoryJSONRequest(ctx, http.MethodPost, withQuery(wecomAPI+"/wedrive/"+action, query), input)
	if err != nil {
		return err
	}
	return d.readJSON(ctx, r, out, responseLimit)
}

type wecomDirItem struct {
	ID       string `json:"fileid"`
	Name     string `json:"file_name"`
	Kind     int    `json:"file_type"`
	Status   *int   `json:"file_status"`
	Modified int64  `json:"mtime"`
}

func (d *httpDirectory) listWecom(ctx context.Context, folder, cursor string) ([]directoryEntry, string, error) {
	const limit = 50
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > 1_000_000 {
			return nil, "", errInvalid
		}
		start = n
	}
	input := map[string]any{"userid": d.config.Settings.Operator, "spaceid": d.config.Settings.Container, "fatherid": folder,
		"sort_type": 1, "start": start, "limit": limit}
	var out struct {
		Code    *int            `json:"errcode"`
		Files   json.RawMessage `json:"file_list"`
		HasMore *bool           `json:"has_more"`
		Next    int             `json:"next_start"`
	}
	if err := d.wecomDrive(ctx, "file_list", input, &out); err != nil {
		return nil, "", err
	}
	if err := wecomStatus(out.Code); err != nil {
		return nil, "", err
	}
	var nodes []wecomDirItem
	if len(out.Files) > 0 && out.Files[0] == '{' {
		var list struct {
			Item []wecomDirItem
		}
		if json.Unmarshal(out.Files, &list) != nil {
			return nil, "", errInvalid
		}
		nodes = list.Item
	} else if len(out.Files) > 0 && json.Unmarshal(out.Files, &nodes) != nil {
		return nil, "", errInvalid
	}
	list := make([]directoryEntry, 0, len(nodes))
	for _, node := range nodes {
		if !validRemoteToken(node.ID) || (node.Status != nil && *node.Status != 1) {
			continue
		}
		item := directoryEntry{ID: node.ID, Title: node.Name}
		if node.Modified > 0 {
			item.Revision = strconv.FormatInt(node.Modified, 10)
		}
		if node.Kind == 1 {
			item.FolderID = node.ID
		} else {
			item.Media = directoryMedia(node.Name)
		}
		list = append(list, item)
	}
	if (out.HasMore != nil && *out.HasMore) || (out.HasMore == nil && len(nodes) == limit) {
		if out.Next <= start {
			out.Next = start + len(nodes)
		}
		if out.Next <= start {
			return nil, "", errInvalid
		}
		return list, strconv.Itoa(out.Next), nil
	}
	return list, "", nil
}

func (d *httpDirectory) openWecom(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	var out struct {
		Code        *int   `json:"errcode"`
		URL         string `json:"download_url"`
		CookieName  string `json:"cookie_name"`
		CookieValue string `json:"cookie_value"`
	}
	if err := d.wecomDrive(ctx, "file_download", map[string]string{"userid": d.config.Settings.Operator, "fileid": item.ID}, &out); err != nil {
		return nil, err
	}
	if err := wecomStatus(out.Code); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(out.URL)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".weixin.qq.com") {
		return nil, errEgress
	}
	headers := make(http.Header)
	if out.CookieName != "" || out.CookieValue != "" {
		cookie := http.Cookie{Name: out.CookieName, Value: out.CookieValue}
		if cookie.Valid() != nil || len(out.CookieValue) > 4096 {
			return nil, errInvalid
		}
		headers.Set("Cookie", cookie.String())
	}
	return d.downloadURL(ctx, out.URL, headers)
}
