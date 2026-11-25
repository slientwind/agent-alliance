package capability

import "slices"

const (
	Knowledge = "knowledge"
	Office    = "office"
	Images    = "images"
	Search    = "search"
	External  = "external"
)

type Item struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var catalog = []Item{
	{Knowledge, "企业知识库"},
	{Office, "表格与文档处理"},
	{Images, "识图与生图"},
	{Search, "联网搜索"},
	{External, "已授权外部 Agent"},
}

func Items() []Item { return slices.Clone(catalog) }

func Valid(ids []string) bool {
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] || !slices.ContainsFunc(catalog, func(item Item) bool { return item.ID == id }) {
			return false
		}
		seen[id] = true
	}
	return true
}

// Unrestricted runs created before capability selection retain their original
// tools. New image operations require an explicit opt-in.
func Allows(ids []string, id string) bool {
	if ids == nil {
		return id != Images
	}
	return slices.Contains(ids, id)
}
