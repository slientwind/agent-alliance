package knowledgesync

import (
	"context"
	"net/http"
	"net/url"
)

func (d *httpDirectory) confluenceACL(ctx context.Context, item directoryEntry) ([][]string, error) {
	ids := []string{}
	if item.ParentID != "" {
		query := url.Values{"limit": {"30"}}
		r, err := directoryJSONRequest(ctx, http.MethodGet,
			withQuery(d.config.Settings.Origin+confluenceAPI+"/pages/"+item.ID+"/ancestors", query), nil)
		if err != nil {
			return nil, err
		}
		r.SetBasicAuth(d.config.Settings.Account, d.secret.Token)
		var out struct {
			Results []struct{ ID string }
			Links   struct{ Next string } `json:"_links"`
		}
		if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
			return nil, err
		}
		if out.Links.Next != "" || len(out.Results) == 0 || len(out.Results) > 30 || out.Results[len(out.Results)-1].ID != item.ParentID {
			return nil, errInvalid
		}
		for _, ancestor := range out.Results {
			if !numericID.MatchString(ancestor.ID) {
				return nil, errInvalid
			}
			ids = append(ids, ancestor.ID)
		}
	}
	ids = append(ids, item.ID)
	groups := [][]string{{"space:" + d.config.Root}}
	for _, id := range ids {
		grants, err := d.confluenceReadRestriction(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(grants) != 0 {
			groups = append(groups, grants)
		}
	}
	return groups, nil
}

func (d *httpDirectory) confluenceReadRestriction(ctx context.Context, id string) ([]string, error) {
	r, err := directoryJSONRequest(ctx, http.MethodGet,
		d.config.Settings.Origin+"/wiki/rest/api/content/"+id+"/restriction/byOperation/read?limit=200", nil)
	if err != nil {
		return nil, err
	}
	r.SetBasicAuth(d.config.Settings.Account, d.secret.Token)
	var out struct {
		Operation    string
		Restrictions *struct {
			User struct {
				Results []struct{ AccountID string }
				Size    int
			}
			Group struct {
				Results []struct{ ID string }
				Size    int
			}
		}
	}
	if err := d.readJSON(ctx, r, &out, responseLimit); err != nil {
		return nil, err
	}
	if out.Operation != "read" || out.Restrictions == nil {
		return nil, errInvalid
	}
	var members []string
	for _, user := range out.Restrictions.User.Results {
		members = principal(members, "user", user.AccountID)
	}
	for _, group := range out.Restrictions.Group.Results {
		members = principal(members, "group", group.ID)
	}
	if len(members) == 0 && (out.Restrictions.User.Size != 0 || out.Restrictions.Group.Size != 0 ||
		len(out.Restrictions.User.Results) != 0 || len(out.Restrictions.Group.Results) != 0) {
		return nil, errInvalid
	}
	return members, nil
}
