package knowledgesync

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (d *httpDirectory) feishuToken(ctx context.Context) (string, error) {
	r, err := directoryJSONRequest(ctx, http.MethodPost, feishuAPI+"/auth/v3/tenant_access_token/internal", map[string]string{
		"app_id": d.config.Settings.Account, "app_secret": d.secret.Token,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Code  *int
		Token string `json:"tenant_access_token"`
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return "", err
	}
	if out.Code == nil || *out.Code != 0 || !validRemoteID(out.Token, 4096) {
		return "", errDenied
	}
	return out.Token, nil
}

func feishuListQuery(cursor string) url.Values {
	query := url.Values{"page_size": {"50"}}
	if cursor != "" {
		query.Set("page_token", cursor)
	}
	return query
}

func feishuNext(hasMore bool, pageToken, nextPageToken string) (string, error) {
	if nextPageToken == "" {
		nextPageToken = pageToken
	}
	if hasMore && nextPageToken == "" {
		return "", errInvalid
	}
	if !hasMore {
		return "", nil
	}
	return nextPageToken, nil
}

func feishuStatus(code *int) error {
	if code == nil {
		return errInvalid
	}
	switch *code {
	case 0:
		return nil
	case 99991661, 99991663, 99991668, 131006, 131013, 1770032, 1770002, 1061002, 1061004, 1062507:
		return errDenied
	default:
		return errUnavailable
	}
}

func (d *httpDirectory) listFeishuWiki(ctx context.Context, parent, cursor string) ([]directoryEntry, string, error) {
	query := feishuListQuery(cursor)
	if parent != "" {
		query.Set("parent_node_token", parent)
	}
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(feishuAPI+"/wiki/v2/spaces/"+d.config.Root+"/nodes", query), nil)
	if err != nil {
		return nil, "", err
	}
	bearer(r, d.token)
	var out struct {
		Code *int
		Data struct {
			More  bool   `json:"has_more"`
			Token string `json:"page_token"`
			Items []struct {
				NodeID   string `json:"node_token"`
				ObjectID string `json:"obj_token"`
				Kind     string `json:"obj_type"`
				Title    string
				Modified string `json:"obj_edit_time"`
				HasChild bool   `json:"has_child"`
				NodeType string `json:"node_type"`
			} `json:"items"`
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, "", err
	}
	if err := feishuStatus(out.Code); err != nil {
		return nil, "", err
	}
	list := make([]directoryEntry, 0, len(out.Data.Items))
	for _, node := range out.Data.Items {
		// Shortcuts can point outside the configured knowledge space.
		if node.NodeType == "shortcut" {
			continue
		}
		item := feishuEntry(node.ObjectID, node.Title, node.Kind, node.Modified)
		if node.HasChild && documentID.MatchString(node.NodeID) {
			item.FolderID = node.NodeID
		}
		list = append(list, item)
	}
	next, err := feishuNext(out.Data.More, out.Data.Token, "")
	return list, next, err
}

func (d *httpDirectory) listFeishuDrive(ctx context.Context, parent, cursor string) ([]directoryEntry, string, error) {
	query := feishuListQuery(cursor)
	query.Set("folder_token", parent)
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(feishuAPI+"/drive/v1/files", query), nil)
	if err != nil {
		return nil, "", err
	}
	bearer(r, d.token)
	var out struct {
		Code *int
		Data struct {
			More  bool   `json:"has_more"`
			Token string `json:"next_page_token"`
			Files []struct {
				Token, Name, Type string
				Modified          string `json:"modified_time"`
			}
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, "", err
	}
	if err := feishuStatus(out.Code); err != nil {
		return nil, "", err
	}
	list := make([]directoryEntry, 0, len(out.Data.Files))
	for _, node := range out.Data.Files {
		item := feishuEntry(node.Token, node.Name, node.Type, node.Modified)
		if node.Type == "folder" && documentID.MatchString(node.Token) {
			item.FolderID = node.Token
		}
		list = append(list, item)
	}
	next, err := feishuNext(out.Data.More, "", out.Data.Token)
	return list, next, err
}

func feishuEntry(id, title, kind, modified string) directoryEntry {
	item := directoryEntry{ID: id, Title: title, Kind: kind, Revision: modified}
	if !documentID.MatchString(id) {
		return item
	}
	switch kind {
	case "docx":
		item.Media = "text/plain"
	case "file":
		item.Media = directoryMedia(title)
	}
	return item
}

func (d *httpDirectory) openFeishu(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	if item.Kind == "file" {
		r, err := directoryJSONRequest(ctx, http.MethodGet, feishuAPI+"/drive/v1/files/"+item.ID+"/download", nil)
		if err != nil {
			return nil, err
		}
		bearer(r, d.token)
		return d.readFile(ctx, r, true)
	}
	r, err := directoryJSONRequest(ctx, http.MethodGet, feishuAPI+"/docx/v1/documents/"+item.ID+"/raw_content", nil)
	if err != nil {
		return nil, err
	}
	bearer(r, d.token)
	var out struct {
		Code *int
		Data struct{ Content *string }
	}
	if err := d.readJSON(ctx, r, &out, 32<<20); err != nil {
		return nil, err
	}
	if err := feishuStatus(out.Code); err != nil {
		return nil, err
	}
	if out.Data.Content == nil {
		return nil, errInvalid
	}
	return io.NopCloser(strings.NewReader(*out.Data.Content)), nil
}

func (d *httpDirectory) listConfluence(ctx context.Context, cursor string) ([]directoryEntry, string, error) {
	query := url.Values{"limit": {"50"}, "status": {"current"}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(d.config.Settings.Origin+confluenceAPI+"/spaces/"+d.config.Root+"/pages", query), nil)
	if err != nil {
		return nil, "", err
	}
	r.SetBasicAuth(d.config.Settings.Account, d.secret.Token)
	var out struct {
		Results []struct {
			ID, Title, Status, SpaceID, ParentID string
			Version                              struct{ Number int }
		}
		Links struct{ Next string } `json:"_links"`
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, "", err
	}
	list := make([]directoryEntry, 0, len(out.Results))
	for _, node := range out.Results {
		if node.Status != "current" || node.SpaceID != d.config.Root || !numericID.MatchString(node.ID) {
			continue
		}
		item := directoryEntry{ID: node.ID, Title: node.Title, Media: "text/html"}
		if node.Version.Number > 0 {
			item.Revision = strconv.Itoa(node.Version.Number)
		}
		if numericID.MatchString(node.ParentID) {
			item.ParentID = node.ParentID
		}
		list = append(list, item)
	}
	next, err := nextFromLink(out.Links.Next, r.URL, "cursor")
	return list, next, err
}

func (d *httpDirectory) openConfluence(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	r, err := directoryJSONRequest(ctx, http.MethodGet, d.config.Settings.Origin+confluenceAPI+"/pages/"+item.ID+"?body-format=storage", nil)
	if err != nil {
		return nil, err
	}
	r.SetBasicAuth(d.config.Settings.Account, d.secret.Token)
	var out struct {
		ID, Status, SpaceID string
		Version             struct{ Number int }
		Body                struct{ Storage struct{ Value *string } }
	}
	if err := d.readJSON(ctx, r, &out, 32<<20); err != nil {
		return nil, err
	}
	if out.ID != item.ID || out.Status != "current" || out.SpaceID != d.config.Root || out.Body.Storage.Value == nil {
		return nil, errDenied
	}
	if item.Revision != "" && item.Revision != strconv.Itoa(out.Version.Number) {
		return nil, errUnavailable
	}
	return io.NopCloser(strings.NewReader(*out.Body.Storage.Value)), nil
}
