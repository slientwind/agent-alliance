package knowledgesync

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func (d *httpDirectory) graphToken(ctx context.Context) (string, error) {
	cfg := d.config.Settings
	if cfg.Tenant == "" {
		return d.secret.Token, nil
	}
	form := url.Values{
		"client_id": {cfg.Account}, "client_secret": {d.secret.Token},
		"scope": {"https://graph.microsoft.com/.default"}, "grant_type": {"client_credentials"},
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/"+cfg.Tenant+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errInvalid
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		Access string `json:"access_token"`
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return "", err
	}
	if !validRemoteID(out.Access, 4096) {
		return "", errDenied
	}
	return out.Access, nil
}

func (d *httpDirectory) graphItem(id string) string {
	drive := graphAPI + "/drives/" + url.PathEscape(d.config.Settings.Container)
	if id == "root" {
		return drive + "/root"
	}
	return drive + "/items/" + url.PathEscape(id)
}

func (d *httpDirectory) listGraph(ctx context.Context, folder, cursor string) ([]directoryEntry, string, error) {
	query := url.Values{"$top": {"50"}, "$select": {"id,name,eTag,cTag,file,folder"}}
	if cursor != "" {
		query.Set("$skiptoken", cursor)
	}
	r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(d.graphItem(folder)+"/children", query), nil)
	if err != nil {
		return nil, "", err
	}
	bearer(r, d.token)
	var out struct {
		Next  string `json:"@odata.nextLink"`
		Value []struct {
			ID, Name, ETag, CTag string
			File, Folder         *struct{}
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, "", err
	}
	list := make([]directoryEntry, 0, len(out.Value))
	for _, node := range out.Value {
		if !validRemoteToken(node.ID) {
			continue
		}
		item := directoryEntry{ID: node.ID, Title: node.Name, Revision: node.CTag}
		if item.Revision == "" {
			item.Revision = node.ETag
		}
		if node.Folder != nil {
			item.FolderID = node.ID
		}
		if node.File != nil {
			item.Media = directoryMedia(node.Name)
		}
		list = append(list, item)
	}
	next, err := nextFromLink(out.Next, r.URL, "$skiptoken")
	return list, next, err
}

func (d *httpDirectory) openGraph(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	r, err := directoryJSONRequest(ctx, http.MethodGet, d.graphItem(item.ID)+"/content", nil)
	if err != nil {
		return nil, err
	}
	bearer(r, d.token)
	return d.readFile(ctx, r, true)
}

func (d *httpDirectory) s3Request(ctx context.Context, key string, query url.Values, revision string) (*http.Request, error) {
	settings := d.config.Settings
	origin := settings.Origin
	if origin == "" {
		domain := "amazonaws.com"
		if strings.HasPrefix(settings.Region, "cn-") {
			domain += ".cn"
		}
		origin = "https://s3." + settings.Region + "." + domain
	}
	u, err := url.Parse(origin)
	if err != nil {
		return nil, errInvalid
	}
	u.Path = "/" + settings.Container
	if key != "" {
		u.Path += "/" + key
	}
	u.RawQuery = query.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errInvalid
	}
	if revision != "" {
		r.Header.Set("If-Match", revision)
	}
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	r.Header.Set("X-Amz-Content-Sha256", emptyHash)
	signer := v4.NewSigner(func(options *v4.SignerOptions) { options.DisableURIPathEscaping = true })
	if err := signer.SignHTTP(ctx, aws.Credentials{
		AccessKeyID: d.secret.KeyID, SecretAccessKey: d.secret.KeySecret, SessionToken: d.secret.SessionToken,
	}, r, emptyHash, "s3", settings.Region, time.Now().UTC()); err != nil {
		return nil, errInvalid
	}
	return r, nil
}

func (d *httpDirectory) listS3(ctx context.Context, prefix, cursor string) ([]directoryEntry, string, error) {
	if !strings.HasPrefix(prefix, d.config.Root) {
		return nil, "", errInvalid
	}
	query := url.Values{"list-type": {"2"}, "prefix": {prefix}, "delimiter": {"/"}, "max-keys": {"50"}, "encoding-type": {"url"}}
	if cursor != "" {
		query.Set("continuation-token", cursor)
	}
	r, err := d.s3Request(ctx, "", query, "")
	if err != nil {
		return nil, "", err
	}
	resp, err := d.request(ctx, r)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := directoryHTTPStatus(resp.StatusCode); err != nil {
		return nil, "", err
	}
	var out struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		IsTruncated           bool
		NextContinuationToken string
		Contents              []struct{ Key, ETag string }
		CommonPrefixes        []struct{ Prefix string }
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, responseLimit)).Decode(&out); err != nil {
		return nil, "", errInvalid
	}
	list := make([]directoryEntry, 0, len(out.Contents)+len(out.CommonPrefixes))
	for _, file := range out.Contents {
		key, err := url.PathUnescape(file.Key)
		if err != nil || !strings.HasPrefix(key, prefix) {
			return nil, "", errInvalid
		}
		name := strings.TrimPrefix(key, prefix)
		if name == "" || strings.ContainsRune(name, '/') {
			continue
		}
		list = append(list, directoryEntry{ID: key, Title: name, Revision: file.ETag, Media: directoryMedia(name)})
	}
	for _, dir := range out.CommonPrefixes {
		key, err := url.PathUnescape(dir.Prefix)
		if err != nil || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "/") || key == prefix {
			return nil, "", errInvalid
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "/")
		if name == "" || strings.ContainsRune(name, '/') {
			return nil, "", errInvalid
		}
		list = append(list, directoryEntry{Title: name, FolderID: key})
	}
	next, err := feishuNext(out.IsTruncated, out.NextContinuationToken, "")
	return list, next, err
}

func (d *httpDirectory) openS3(ctx context.Context, item directoryEntry) (io.ReadCloser, error) {
	if !strings.HasPrefix(item.ID, d.config.Root) || path.Base(item.ID) != item.Title {
		return nil, errInvalid
	}
	r, err := d.s3Request(ctx, item.ID, nil, item.Revision)
	if err != nil {
		return nil, err
	}
	return d.readFile(ctx, r, false)
}
