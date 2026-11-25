package knowledgesync

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

func validPrincipal(raw string) bool {
	kind, key, found := strings.Cut(raw, ":")
	return found && len(raw) <= 300 && strings.TrimSpace(key) == key && validRemoteID(key, 290) &&
		slices.Contains([]string{"user", "group", "department", "chat", "space", "site_user", "site_group", "canonical", "email"}, kind)
}

func principal(out []string, kind, key string) []string {
	value := kind + ":" + key
	if validPrincipal(value) {
		return append(out, value)
	}
	return out
}

func verifiedACL(groups [][]string) ([][]string, bool) {
	if len(groups) == 0 || len(groups) > 32 {
		return [][]string{}, false
	}
	for i, group := range groups {
		if len(group) == 0 || len(group) > 1000 {
			return [][]string{}, false
		}
		for _, p := range group {
			if !validPrincipal(p) {
				return [][]string{}, false
			}
		}
		slices.Sort(group)
		groups[i] = slices.Compact(group)
	}
	return groups, true
}

func (d *httpDirectory) SourceACL(ctx context.Context, item directoryEntry) ([][]string, error) {
	if d.config.Provider == "confluence" {
		return d.confluenceACL(ctx, item)
	}
	var members []string
	var err error
	switch d.config.Provider {
	case "feishu_wiki", "feishu_drive":
		members, err = d.feishuACL(ctx, item)
		if err == nil && d.config.Provider == "feishu_wiki" {
			return [][]string{{"space:" + d.config.Root}, members}, nil
		}
	case "dingtalk":
		members, err = d.dingtalkACL(ctx, item)
	case "wecom":
		members, err = d.wecomACL(ctx, item)
	case "sharepoint":
		members, err = d.graphACL(ctx, item)
	case "s3":
		members, err = d.s3ACL(ctx, item)
	default:
		err = errInvalid
	}
	return [][]string{members}, err
}

func collectACL(fetch func(string) ([]string, string, error)) ([]string, error) {
	var result []string
	cursor := ""
	seen := map[string]bool{}
	for range 20 {
		members, next, err := fetch(cursor)
		if err != nil {
			return nil, err
		}
		result = append(result, members...)
		if next == "" {
			return result, nil
		}
		if seen[next] || !validRemoteID(next, 4096) {
			return nil, errInvalid
		}
		seen[next], cursor = true, next
	}
	return nil, errInvalid
}

func (d *httpDirectory) feishuACL(ctx context.Context, item directoryEntry) ([]string, error) {
	return collectACL(func(cursor string) ([]string, string, error) {
		query := feishuListQuery(cursor)
		query.Set("type", item.Kind)
		r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(feishuAPI+"/drive/v1/permissions/"+item.ID+"/members", query), nil)
		if err != nil {
			return nil, "", err
		}
		bearer(r, d.token)
		var out struct {
			Code *int
			Data struct {
				HasMore bool   `json:"has_more"`
				Cursor  string `json:"page_token"`
				Items   []struct {
					ID   string `json:"member_id"`
					Type string `json:"member_type"`
					Perm string
				}
			}
		}
		if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
			return nil, "", err
		}
		if err := feishuStatus(out.Code); err != nil {
			return nil, "", err
		}
		var members []string
		for _, access := range out.Data.Items {
			if !slices.Contains([]string{"view", "edit", "full_access", "owner"}, access.Perm) {
				continue
			}
			switch access.Type {
			case "openid", "open_id", "unionid", "union_id", "userid", "user_id":
				members = principal(members, "user", access.ID)
			case "opendepartmentid", "open_department_id", "departmentid", "department_id":
				members = principal(members, "department", access.ID)
			case "openchat", "open_chat", "chatid", "chat_id":
				members = principal(members, "chat", access.ID)
			case "email":
				members = principal(members, "email", access.ID)
			}
		}
		next, err := feishuNext(out.Data.HasMore, out.Data.Cursor, "")
		return members, next, err
	})
}

func (d *httpDirectory) dingtalkACL(ctx context.Context, item directoryEntry) ([]string, error) {
	return collectACL(func(cursor string) ([]string, string, error) {
		query := url.Values{"unionId": {d.config.Settings.Operator}}
		options := map[string]any{"maxResults": 50}
		if cursor != "" {
			options["nextToken"] = cursor
		}
		r, err := directoryJSONRequest(ctx, http.MethodPost, withQuery(d.dingDirectoryPath()+"/"+url.PathEscape(item.ID)+"/permissions/query", query),
			map[string]any{"option": options})
		if err != nil {
			return nil, "", err
		}
		r.Header.Set("x-acs-dingtalk-access-token", d.token)
		var out struct {
			NextToken   string
			Permissions []struct {
				Member   struct{ ID, Type string }
				Role     struct{ ID string }
				Duration int64
			}
		}
		if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
			return nil, "", err
		}
		var members []string
		for _, access := range out.Permissions {
			if !slices.Contains([]string{"OWNER", "MANAGER", "EDITOR", "VIEWER", "READER", "BROWSER"}, strings.ToUpper(access.Role.ID)) {
				continue
			}
			if access.Duration > 0 {
				// Temporary grants must not outlive the next upstream refresh.
				continue
			}
			switch strings.ToUpper(access.Member.Type) {
			case "USER":
				members = principal(members, "user", access.Member.ID)
			case "DEPT", "DEPARTMENT":
				members = principal(members, "department", access.Member.ID)
			case "GROUP", "CHAT":
				members = principal(members, "group", access.Member.ID)
			}
		}
		return members, out.NextToken, nil
	})
}

func (d *httpDirectory) wecomACL(ctx context.Context, item directoryEntry) ([]string, error) {
	type grant struct {
		Type       int    `json:"type"`
		User       string `json:"userid"`
		Department int64  `json:"departmentid"`
		Auth       int    `json:"auth"`
	}
	var out struct {
		Code      *int    `json:"errcode"`
		Direct    []grant `json:"file_member_list"`
		Inherited struct {
			Enabled bool    `json:"inherit"`
			Grants  []grant `json:"auth_list"`
		} `json:"inherit_father_auth"`
	}
	if err := d.wecomDrive(ctx, "get_file_permission", map[string]string{"fileid": item.ID}, &out); err != nil {
		return nil, err
	}
	if err := wecomStatus(out.Code); err != nil {
		return nil, err
	}
	var members []string
	if out.Inherited.Enabled {
		out.Direct = append(out.Direct, out.Inherited.Grants...)
	}
	for _, access := range out.Direct {
		if access.Auth < 1 || access.Auth > 3 {
			continue
		}
		if access.Type == 1 {
			members = principal(members, "user", access.User)
		} else if access.Type == 2 && access.Department > 0 {
			members = principal(members, "department", strconv.FormatInt(access.Department, 10))
		}
	}
	return members, nil
}

type graphACLIdentity struct {
	User, Group, SiteUser, SiteGroup struct{ ID string }
}

func (d *httpDirectory) graphACL(ctx context.Context, item directoryEntry) ([]string, error) {
	return collectACL(func(cursor string) ([]string, string, error) {
		query := url.Values{"$top": {"50"}}
		if cursor != "" {
			query.Set("$skiptoken", cursor)
		}
		r, err := directoryJSONRequest(ctx, http.MethodGet, withQuery(d.graphItem(item.ID)+"/permissions", query), nil)
		if err != nil {
			return nil, "", err
		}
		bearer(r, d.token)
		var out struct {
			Next  string `json:"@odata.nextLink"`
			Value []struct {
				Roles                 []string
				GrantedToV2           graphACLIdentity
				GrantedToIdentitiesV2 []graphACLIdentity
				Expiration            *time.Time `json:"expirationDateTime"`
				Link                  *struct{}
			}
		}
		if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
			return nil, "", err
		}
		var members []string
		for _, access := range out.Value {
			if (access.Expiration != nil && !access.Expiration.IsZero()) || access.Link != nil ||
				!(slices.Contains(access.Roles, "read") || slices.Contains(access.Roles, "write") || slices.Contains(access.Roles, "owner")) {
				continue
			}
			identities := append(access.GrantedToIdentitiesV2, access.GrantedToV2)
			for _, id := range identities {
				members = principal(members, "user", id.User.ID)
				members = principal(members, "group", id.Group.ID)
				members = principal(members, "site_user", id.SiteUser.ID)
				members = principal(members, "site_group", id.SiteGroup.ID)
			}
		}
		next, err := nextFromLink(out.Next, r.URL, "$skiptoken")
		return members, next, err
	})
}

func (d *httpDirectory) s3ACL(ctx context.Context, item directoryEntry) ([]string, error) {
	r, err := d.s3Request(ctx, item.ID, url.Values{"acl": {""}}, "")
	if err != nil {
		return nil, err
	}
	resp, err := d.request(ctx, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := directoryHTTPStatus(resp.StatusCode); err != nil {
		return nil, err
	}
	var out struct {
		XMLName xml.Name `xml:"AccessControlPolicy"`
		Grants  []struct {
			Permission string
			Grantee    struct{ ID, URI, EmailAddress string }
		} `xml:"AccessControlList>Grant"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, responseLimit)).Decode(&out); err != nil {
		return nil, errInvalid
	}
	var members []string
	for _, g := range out.Grants {
		if g.Permission == "READ" || g.Permission == "FULL_CONTROL" {
			members = principal(members, "canonical", g.Grantee.ID)
			members = principal(members, "group", g.Grantee.URI)
			members = principal(members, "email", g.Grantee.EmailAddress)
		}
	}
	return members, nil
}
