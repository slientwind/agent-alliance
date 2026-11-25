package ecosystem

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"alliance/internal/ecosysteminspect"
	"alliance/internal/skills"
	"golang.org/x/sync/errgroup"
)

var errInspection = errors.New("生态体检服务暂时不可用")

type treeItem struct{ Path, Type, SHA, Mode string }

type gitContents struct {
	files     map[string]string
	license   string
	inventory []string
	problems  []string
}

func collectGitContents(entry string, tree []treeItem) gitContents {
	out := gitContents{files: map[string]string{}}
	prefix := ""
	if folder := path.Dir(entry); folder != "." {
		prefix = folder + "/"
	}
	var rootLicense string
	for _, item := range tree {
		lower := strings.ToLower(item.Path)
		if lower == "license" || lower == "license.md" || lower == "license.txt" {
			if item.Type == "blob" && item.Mode == "100644" && objectID.MatchString(item.SHA) {
				rootLicense = item.SHA
			}
		}
		if !strings.HasPrefix(item.Path, prefix) || item.Type == "tree" {
			continue
		}
		name := strings.TrimPrefix(item.Path, prefix)
		first, _, _ := strings.Cut(name, "/")
		if prefix == "" && first != name && first != "references" && first != "resources" && first != "scripts" {
			continue
		}
		if len(out.inventory) >= 240 {
			out.problems = append(out.problems, "entry_size_limit")
			break
		}
		if !safeGitPath(name) {
			out.problems = appendUnique(out.problems, "invalid_path")
			continue
		}
		out.inventory = append(out.inventory, name)
		if item.Type != "blob" || item.Mode != "100644" || !objectID.MatchString(item.SHA) {
			out.problems = appendUnique(out.problems, "symlink_or_special_file")
			continue
		}
		switch strings.ToLower(name) {
		case "license", "license.md", "license.txt":
			out.license = item.SHA
		case "skill.md", "manifest.json":
			out.files[name] = item.SHA
		default:
			if first != name && (first == "references" || first == "resources") &&
				slices.Contains([]string{".md", ".txt", ".json", ".csv"}, path.Ext(name)) {
				out.files[name] = item.SHA
			}
		}
	}
	if out.license == "" {
		out.license = rootLicense
	}
	if len(out.files) > 16 {
		out.problems = appendUnique(out.problems, "entry_size_limit")
	}
	return out
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func (f fetcher) gitBlob(ctx context.Context, src source, ref string) ([]byte, error) {
	var blob struct {
		Encoding, Content, SHA string
		Size                   int
	}
	if !objectID.MatchString(ref) {
		return nil, errDiscovery
	}
	if err := f.get(ctx, src, appendAPI(src.Endpoint, "git", "blobs", ref), &blob); err != nil {
		return nil, err
	}
	if blob.Encoding != "base64" || blob.SHA != ref || blob.Size < 1 || blob.Size > skills.FileLimit {
		return nil, errDiscovery
	}
	data, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil || len(data) != blob.Size || gitObjectHash(data, len(ref)) != ref {
		return nil, errDiscovery
	}
	return data, nil
}

func gitObjectHash(data []byte, length int) string {
	object := append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)
	if length == 40 {
		digest := sha1.Sum(object)
		return hex.EncodeToString(digest[:])
	}
	digest := sha256.Sum256(object)
	return hex.EncodeToString(digest[:])
}

type inspectionClient struct {
	URL, Token string
}

func (client inspectionClient) check(ctx context.Context, input ecosysteminspect.Input) (ecosysteminspect.Result, error) {
	var result ecosysteminspect.Result
	requestURL, err := url.Parse(client.URL)
	if err != nil || requestURL.Host == "" || requestURL.User != nil || requestURL.RawQuery != "" ||
		requestURL.Fragment != "" || (requestURL.Scheme != "http" && requestURL.Scheme != "https") || client.Token == "" {
		return result, errInspection
	}
	requestURL.Path = strings.TrimSuffix(requestURL.Path, "/") + "/inspect"
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > ecosysteminspect.RequestLimit {
		return result, errInspection
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return result, errInspection
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+client.Token)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	out := &http.Client{Timeout: 9 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := out.Do(r)
	if err != nil {
		return result, errInspection
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ecosysteminspect.RequestLimit+1))
	if resp.StatusCode != 200 || err != nil || len(raw) > ecosysteminspect.RequestLimit || json.Unmarshal(raw, &result) != nil ||
		!slices.Contains([]string{"clean", "review_required", "blocked"}, result.Report.Status) {
		return ecosysteminspect.Result{}, errInspection
	}
	if result.Report.Status == "blocked" {
		result.Hash, result.Bytes = "", nil
	} else if input.Kind == "skill" {
		digest := sha256.Sum256(result.Bytes)
		if len(result.Bytes) < 1 || len(result.Bytes) > skills.ArchiveLimit || result.Hash != hex.EncodeToString(digest[:]) {
			return ecosysteminspect.Result{}, errInspection
		}
	} else if result.Hash != "" || len(result.Bytes) != 0 {
		return ecosysteminspect.Result{}, errInspection
	}
	return result, nil
}

func (f fetcher) inspectBatch(ctx context.Context, src source, batch *feedBatch, client inspectionClient) error {
	group, scope := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for i := range batch.Items {
		item := &batch.Items[i]
		group.Go(func() error {
			metadata, err := json.Marshal(item.Metadata)
			if err != nil {
				return err
			}
			input := ecosysteminspect.Input{Kind: item.Kind, Updated: item.Updated, Description: item.Description, Metadata: metadata}
			if item.Kind == "skill" {
				// Repository and content routes are configured provenance. The
				// inspector scans the downloaded package instructions instead.
				input.Metadata = nil
				input.Inventory = item.git.inventory
				input.Problems = item.git.problems
				if len(input.Problems) == 0 {
					input.Files = make(map[string][]byte, len(item.git.files))
					for name, blob := range item.git.files {
						content, err := f.gitBlob(scope, src, blob)
						if err != nil {
							return err
						}
						input.Files[name] = content
					}
					if item.git.license != "" {
						license, err := f.gitBlob(scope, src, item.git.license)
						if err != nil {
							return err
						}
						input.License = string(license)
					}
				}
			}
			item.inspection, err = client.check(scope, input)
			return err
		})
	}
	return group.Wait()
}
