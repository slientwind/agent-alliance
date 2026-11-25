package knowledge

import (
	"alliance/internal/web"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

type fileRef struct {
	hash, name, media         string
	previewHash, previewMedia string
}
type fileResolver func() (fileRef, error)

func (s Server) authorizedFile(w http.ResponseWriter, r *http.Request, resolve fileResolver) error {
	ref, err := resolve()
	if err != nil {
		return err
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("preview") != "1" {
		return s.sendFile(w, ref.hash, ref.name, ref.media)
	}
	data, media, err := s.Objects.renderPreview(r.Context(), ref)
	if err != nil {
		return err
	}
	current, err := resolve()
	if err != nil {
		return err
	}
	if current.hash != ref.hash || current.name != ref.name || current.media != ref.media {
		return web.Fail(409, "预览期间文件引用已变化")
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "attachment")
	_, err = w.Write(data)
	return err
}

func (o Objects) renderPreview(ctx context.Context, ref fileRef) ([]byte, string, error) {
	if ref.previewHash != "" {
		preview, err := o.Open(ref.previewHash)
		if err == nil {
			defer preview.Close()
			data, err := io.ReadAll(io.LimitReader(preview, (16<<20)+1))
			if err == nil && len(data) <= 16<<20 {
				return data, ref.previewMedia, nil
			}
		}
	}
	blob, err := o.Open(ref.hash)
	if err != nil {
		return nil, "", err
	}
	defer blob.Close()
	data, err := io.ReadAll(io.LimitReader(blob, AttachmentLimit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > AttachmentLimit {
		return nil, "", web.Fail(413, "文件超出预览大小限制")
	}
	media := http.DetectContentType(data)
	switch strings.ToLower(path.Ext(ref.name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		if media != "image/png" && media != "image/jpeg" && media != "image/webp" {
			return nil, "", web.Fail(422, "图像类型与内容不符")
		}
	case ".pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return nil, "", web.Fail(422, "PDF 内容无效")
		}
		media = "application/pdf"
	case ".docx", ".xlsx", ".pptx":
		data, err = convertOffice(ctx, strings.ToLower(path.Ext(ref.name))[1:], data)
		if err != nil {
			return nil, "", err
		}
		media = "application/pdf"
	default:
		if !strings.HasPrefix(ref.media, "text/plain") && !strings.HasPrefix(ref.media, "text/csv") && !strings.HasPrefix(ref.media, "text/tab-separated-values") {
			return nil, "", web.Fail(415, "此格式支持下载原件")
		}
		media = "text/plain; charset=utf-8"
	}
	return data, media, nil
}

func previewRequest(ctx context.Context, route string, data []byte) (*http.Response, error) {
	service := os.Getenv("PREVIEW_URL")
	if service == "" {
		service = "http://127.0.0.1:18727"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(service, "/")+route, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CONNECTOR_TOKEN"))
	client := http.Client{Timeout: 28 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}

func convertOffice(ctx context.Context, ext string, data []byte) ([]byte, error) {
	res, err := previewRequest(ctx, "/convert/"+ext, data)
	if err != nil {
		return nil, web.Fail(503, "预览服务暂不可用，可下载原件")
	}
	defer res.Body.Close()
	if res.StatusCode == 422 || res.StatusCode == 413 {
		return nil, web.Fail(422, "此文件超出转换范围，可能含外部引用、嵌入对象或复杂内容，可下载原件")
	}
	if res.StatusCode != 200 {
		return nil, web.Fail(503, "文件转换未完成，可下载原件")
	}
	converted, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(converted) > 16<<20 || !bytes.HasPrefix(converted, []byte("%PDF-")) {
		return nil, web.Fail(422, "文件转换未产生可用的 PDF")
	}
	return converted, nil
}
