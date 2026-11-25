package ecosystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"alliance/internal/egress"
	"alliance/internal/resources"
	"alliance/internal/web"
)

const feedBodyLimit = 4 << 20
const maxFeedPages = 8
const maxGitMatches = 200

var errDiscovery = errors.New("上游目录暂时无法读取")

type source struct {
	ID, Kind, Endpoint, Cursor string
	Revision                   int
	Config                     SourceConfig
	Credential                 []byte
}

type candidate struct {
	Key, Version, Digest, Kind, Name, Description, Homepage, PackageURL string
	Metadata                                                            map[string]any
	Updated                                                             *time.Time
}

type feedBatch struct {
	Items  []candidate
	Cursor string
}

type fetcher struct {
	Egress  egress.Policy
	Vault   resources.Vault
	HTTP    *http.Client
	Current func(context.Context) bool
}

func (f fetcher) get(ctx context.Context, src source, address string, out any) error {
	base, err := url.Parse(src.Endpoint)
	if err != nil {
		return errDiscovery
	}
	requestURL, err := url.Parse(address)
	if err != nil || requestURL.Scheme != base.Scheme || requestURL.Host != base.Host {
		return errDiscovery
	}
	sub, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	client, err := f.Egress.Client(sub, address, false, f.HTTP)
	if err != nil {
		return errDiscovery
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(sub, http.MethodGet, address, nil)
	if err != nil {
		return errDiscovery
	}
	req.Header.Set("Accept", "application/json")
	if len(src.Credential) > 0 {
		if src.Kind != PrivateGit {
			return errDiscovery
		}
		key, err := f.Vault.Open(src.Credential, credentialScope(src.ID, src.Endpoint))
		if err != nil || key == "" {
			return errDiscovery
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if f.Current != nil && !f.Current(sub) {
		return errDiscovery
	}
	resp, err := client.Do(req)
	if err != nil {
		return errDiscovery
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > feedBodyLimit {
		return errDiscovery
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, feedBodyLimit+1))
	if err != nil || len(raw) > feedBodyLimit || json.Unmarshal(raw, out) != nil {
		return errDiscovery
	}
	return nil
}

func (f fetcher) discovery(ctx context.Context, src source) (feedBatch, error) {
	switch src.Kind {
	case MCPRegistry:
		return f.registry(ctx, src)
	case SkillsGit, PrivateGit:
		return f.git(ctx, src)
	case A2ADirectory:
		return f.directory(ctx, src)
	default:
		return feedBatch{}, errDiscovery
	}
}

func boundedLabel(raw string, limit int) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(raw))
	runes := []rune(cleaned)
	return strings.TrimSpace(string(runes[:min(len(runes), limit)]))
}

func publicLink(raw string) string {
	clean, ok := cleanHTTPS(raw)
	if !ok {
		return ""
	}
	u, _ := url.Parse(clean)
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

func addCandidate(batch *feedBatch, item candidate) error {
	item.Key, item.Version = boundedLabel(item.Key, 256), boundedLabel(item.Version, 256)
	item.Name, item.Description = boundedLabel(item.Name, 160), boundedLabel(item.Description, 4000)
	if item.Key == "" || item.Version == "" || item.Name == "" {
		return nil
	}
	item.Homepage, item.PackageURL = publicLink(item.Homepage), publicLink(item.PackageURL)
	if item.Metadata == nil {
		item.Metadata = map[string]any{}
	}
	wire, err := json.Marshal(item)
	if err != nil {
		return err
	}
	item.Digest = fmt.Sprintf("%x", web.Hash(string(wire)))
	batch.Items = append(batch.Items, item)
	return nil
}

func feedURL(base, cursor string, limit bool) string {
	parsed, _ := url.Parse(base)
	query := parsed.Query()
	if limit {
		query.Set("limit", "100")
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type pageMetadata struct {
	NextCursor string `json:"nextCursor"`
}

type mcpRegistryPage struct {
	Servers []struct {
		Server struct {
			Name, Title, Description, Version string
			WebsiteURL                        string `json:"websiteUrl"`
			Repository                        struct{ URL string }
			Remotes                           []struct {
				Type, URL string
			}
		} `json:"server"`
		Meta struct {
			Official struct {
				Status      string
				PublishedAt *time.Time
			} `json:"io.modelcontextprotocol.registry/official"`
		} `json:"_meta"`
	} `json:"servers"`
	Metadata pageMetadata `json:"metadata"`
}

func (f fetcher) registry(ctx context.Context, src source) (feedBatch, error) {
	return readPages(src.Cursor, func(batch *feedBatch, cursor string) (string, error) {
		var data mcpRegistryPage
		if err := f.get(ctx, src, feedURL(src.Endpoint, cursor, true), &data); err != nil || data.Servers == nil || len(data.Servers) > 250 {
			return "", errDiscovery
		}
		for _, entry := range data.Servers {
			server := entry.Server
			if entry.Meta.Official.Status != "" && entry.Meta.Official.Status != "active" {
				continue
			}
			name := server.Title
			if name == "" {
				name = server.Name
			}
			homepage := server.WebsiteURL
			if homepage == "" {
				homepage = server.Repository.URL
			}
			external := make([]map[string]string, 0, min(len(server.Remotes), 8))
			for _, remote := range server.Remotes {
				if link := publicLink(remote.URL); link != "" && len(external) < 8 {
					external = append(external, map[string]string{"url": link, "transport": boundedLabel(remote.Type, 70)})
				}
			}
			if err := addCandidate(batch, candidate{Key: server.Name, Version: server.Version, Kind: "mcp",
				Name: name, Description: server.Description, Homepage: homepage, Updated: entry.Meta.Official.PublishedAt,
				Metadata: map[string]any{"remoteEndpoints": external}}); err != nil {
				return "", err
			}
		}
		return data.Metadata.NextCursor, nil
	})
}

func readPages(cursor string, load func(*feedBatch, string) (string, error)) (feedBatch, error) {
	var batch feedBatch
	seen := map[string]bool{cursor: true}
	for range maxFeedPages {
		next, err := load(&batch, cursor)
		if err != nil {
			return feedBatch{}, err
		}
		if next == "" {
			return batch, nil
		}
		if len(next) > 2048 || seen[next] {
			return feedBatch{}, errDiscovery
		}
		seen[next] = true
		cursor = next
	}
	batch.Cursor = cursor
	return batch, nil
}

type a2aCard struct {
	Name, Description, Version, URL, ProtocolVersion, PreferredTransport string
	SupportedInterfaces                                                  []struct {
		URL, ProtocolBinding, ProtocolVersion string
	}
}

func (f fetcher) directory(ctx context.Context, src source) (feedBatch, error) {
	return readPages(src.Cursor, func(batch *feedBatch, cursor string) (string, error) {
		var page struct {
			Agents []struct {
				ID       string
				Verified bool
				Card     a2aCard
			}
			Metadata pageMetadata
		}
		if err := f.get(ctx, src, feedURL(src.Endpoint, cursor, false), &page); err != nil || page.Agents == nil || len(page.Agents) > 250 {
			return "", errDiscovery
		}
		for _, listing := range page.Agents {
			if !listing.Verified {
				continue
			}
			card := listing.Card
			endpoint := publicLink(card.URL)
			if endpoint == "" {
				for _, endpointDescription := range card.SupportedInterfaces {
					if endpoint = publicLink(endpointDescription.URL); endpoint != "" {
						card.ProtocolVersion = endpointDescription.ProtocolVersion
						card.PreferredTransport = endpointDescription.ProtocolBinding
						break
					}
				}
			}
			if endpoint == "" {
				continue
			}
			key := listing.ID
			if key == "" {
				key = endpoint
			}
			if err := addCandidate(batch, candidate{Key: key, Version: card.Version, Kind: "agent",
				Name: card.Name, Description: card.Description, Homepage: endpoint,
				Metadata: map[string]any{"endpoint": endpoint, "protocolVersion": boundedLabel(card.ProtocolVersion, 60),
					"transport": boundedLabel(card.PreferredTransport, 70), "verifiedBySource": true}}); err != nil {
				return "", err
			}
		}
		return page.Metadata.NextCursor, nil
	})
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (f fetcher) git(ctx context.Context, src source) (feedBatch, error) {
	var commit struct {
		SHA     string
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Tree      struct{ SHA string }
			Committer struct{ Date *time.Time }
		}
	}
	commits, _ := url.Parse(appendAPI(src.Endpoint, "commits"))
	commits.RawPath = commits.EscapedPath() + "/" + url.PathEscape(src.Config.Ref)
	commits.Path += "/" + src.Config.Ref
	if err := f.get(ctx, src, commits.String(), &commit); err != nil ||
		!objectID.MatchString(commit.SHA) || !objectID.MatchString(commit.Commit.Tree.SHA) {
		return feedBatch{}, errDiscovery
	}
	type treeItem struct{ Path, Type, SHA, Mode string }
	var listing struct {
		Tree      []treeItem
		Truncated bool
	}
	tree := appendAPI(src.Endpoint, "git", "trees", commit.Commit.Tree.SHA) + "?recursive=1"
	if err := f.get(ctx, src, tree, &listing); err != nil || listing.Truncated || listing.Tree == nil {
		return feedBatch{}, errDiscovery
	}
	slices.SortFunc(listing.Tree, func(a, b treeItem) int { return strings.Compare(a.Path, b.Path) })
	version, after, _ := strings.Cut(src.Cursor, ":")
	if version != commit.SHA {
		after = ""
	}
	var batch feedBatch
	var last string
	for _, item := range listing.Tree {
		if item.Path <= after || item.Type != "blob" || item.Mode != "100644" || path.Base(item.Path) != "SKILL.md" ||
			!objectID.MatchString(item.SHA) || len(item.Path) > 256 || !safeGitPath(item.Path) ||
			src.Config.Root != "" && !strings.HasPrefix(item.Path, src.Config.Root+"/") {
			continue
		}
		if len(batch.Items) == maxGitMatches {
			batch.Cursor = commit.SHA + ":" + last
			break
		}
		dir := path.Dir(item.Path)
		name := path.Base(dir)
		if dir == "." {
			u, _ := url.Parse(src.Endpoint)
			name = path.Base(u.Path)
		}
		link := appendAPI(src.Endpoint, "contents", item.Path) + "?ref=" + commit.SHA
		metadata := map[string]any{"repository": src.Endpoint, "filePath": item.Path, "commit": commit.SHA,
			"blob": item.SHA, "contentRoute": link}
		if err := addCandidate(&batch, candidate{Key: item.Path, Version: commit.SHA, Kind: "skill", Name: name,
			Homepage: commit.HTMLURL, PackageURL: link, Metadata: metadata, Updated: commit.Commit.Committer.Date}); err != nil {
			return feedBatch{}, err
		}
		last = item.Path
	}
	return batch, nil
}
