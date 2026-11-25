package knowledge

import (
	"alliance/internal/web"
	"context"
	"encoding/json"
	"io"
)

func OfficeAnchor(ctx context.Context, format string, index int, sheet string, file []byte, replacement []string) ([]string, []byte, error) {
	switch format {
	case "docx", "xlsx", "pptx", "pdf":
	default:
		return nil, nil, web.Fail(422, "此格式不支持局部修改")
	}
	request, err := json.Marshal(struct {
		Index int      `json:"index"`
		Sheet string   `json:"sheet"`
		File  []byte   `json:"file"`
		Parts []string `json:"parts"`
	}{index, sheet, file, replacement})
	if err != nil {
		return nil, nil, err
	}
	res, err := previewRequest(ctx, "/anchor/"+format, request)
	if err != nil {
		return nil, nil, web.Fail(503, "文件处理服务暂不可用")
	}
	defer res.Body.Close()
	if res.StatusCode == 413 || res.StatusCode == 422 {
		return nil, nil, web.Fail(422, "此位置没有可安全修改的文件内容，或超出了修改范围。")
	}
	if res.StatusCode != 200 {
		return nil, nil, web.Fail(503, "文件处理服务暂未完成修改")
	}
	limit := int64(AttachmentLimit)
	if replacement == nil {
		limit = 64 << 10
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, web.Fail(422, "修改后的文件大小超出限制")
	}
	if replacement != nil {
		return nil, data, nil
	}
	var parts []string
	if err := json.Unmarshal(data, &parts); err != nil {
		return nil, nil, err
	}
	return parts, nil, nil
}
