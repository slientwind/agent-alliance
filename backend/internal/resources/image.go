package resources

import (
	"alliance/internal/knowledge"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
)

const ImageOperation = "png"
const ImageEditOperation = "image-edit"

func ProducesPNG(operation string) bool {
	return operation == ImageOperation || operation == ImageEditOperation
}

func RequiresPicture(operation string) bool {
	return operation == VisionOperation || operation == ImageEditOperation
}

var ErrImageFormat = errors.New("图像回执格式错误")

// The synchronous image contract returns original PNG bytes inline. Signed URLs are
// a different contract and must never be fetched using the model's bearer credential.
func (m Model) generateImage(ctx context.Context, messages []Message) (Reply, error) {
	var parts []string
	for _, msg := range messages {
		if prompt := strings.TrimSpace(msg.Content); prompt != "" {
			parts = append(parts, prompt)
		}
	}
	payload := map[string]any{"model": m.Name, "n": 1, "response_format": "b64_json"}
	var base knowledge.Picture
	if m.OutputOperation == ImageEditOperation {
		if len(m.Pictures) != 1 {
			return Reply{}, errors.New("参考图编辑需要恰好一张获准的图片")
		}
		base = m.Pictures[0]
		payload["image"] = "data:" + base.Media + ";base64," + base64.StdEncoding.EncodeToString(base.Data)
		if m.ImageRegion != nil {
			cfg, err := m.ImageRegion.ValidateBase(base)
			if err != nil {
				return Reply{}, err
			}
			r := m.ImageRegion
			payload["size"] = fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
			parts = append(parts, fmt.Sprintf("按原图像素坐标只修改矩形：左上角 (%d, %d)，宽 %d，高 %d。保持原尺寸和画面位置。", r.X, r.Y, r.Width, r.Height))
		}
	}
	payload["prompt"] = strings.Join(parts, "\n\n")
	body, err := json.Marshal(payload)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.Endpoint, "/")+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+m.Key)
	req.Header.Set("Content-Type", "application/json")
	client, err := providerClient()
	if err != nil {
		return Reply{}, err
	}
	imageClient := *client
	imageClient.Timeout = ImageTimeout
	rsp, err := imageClient.Do(req)
	if err != nil {
		return Reply{}, errors.New("图像请求结果待核对")
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		return Reply{}, ProviderStatus(rsp.StatusCode)
	}
	const limit = 8<<20 + 32768
	raw, err := io.ReadAll(io.LimitReader(rsp.Body, limit+1))
	if err != nil {
		return Reply{}, errors.New("图像回执不完整，需核对原调用")
	}
	if len(raw) > limit {
		return Reply{}, fmt.Errorf("%w，超出大小限制", ErrImageFormat)
	}
	var result struct {
		Data []struct {
			PNG []byte `json:"b64_json"`
		}
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Data) != 1 || len(result.Data[0].PNG) > 6<<20 {
		return Reply{}, fmt.Errorf("%w，须返回单张 PNG 原件（上限 6 MiB）", ErrImageFormat)
	}
	data := result.Data[0].PNG
	w, h, err := knowledge.PNGDimensions(bytes.NewReader(data))
	if err != nil {
		return Reply{}, fmt.Errorf("%w，%v", ErrImageFormat, err)
	}
	text := fmt.Sprintf("PNG 图像 · %d × %d 像素", w, h)
	if m.ImageRegion != nil {
		data, err = mergeImageRegion(base.Data, data, *m.ImageRegion)
		if err != nil {
			return Reply{}, err
		}
		text += " · 已按选区合成"
	}
	return Reply{Text: text, File: &File{Name: "image.png", Mime: "image/png", Data: data}}, nil
}

func mergeImageRegion(original, candidate []byte, region knowledge.ImageRegion) ([]byte, error) {
	base, err := png.Decode(bytes.NewReader(original))
	if err != nil {
		return nil, fmt.Errorf("%w，基准 PNG 已损坏", ErrImageFormat)
	}
	edited, err := png.Decode(bytes.NewReader(candidate))
	if err != nil || edited.Bounds() != base.Bounds() {
		return nil, fmt.Errorf("%w，候选图与基准图尺寸不同", ErrImageFormat)
	}
	var output bytes.Buffer
	if err := png.Encode(&output, regionImage{base: base, edited: edited, region: region.Bounds()}); err != nil || output.Len() > 6<<20 {
		return nil, fmt.Errorf("%w，局部合成失败或超过 6 MiB", ErrImageFormat)
	}
	return output.Bytes(), nil
}

type regionImage struct {
	base, edited image.Image
	region       image.Rectangle
}

func (img regionImage) Bounds() image.Rectangle { return img.base.Bounds() }
func (img regionImage) ColorModel() color.Model  { return color.NRGBA64Model }
func (img regionImage) At(x, y int) color.Color {
	source := img.base
	if image.Pt(x, y).In(img.region) {
		source = img.edited
	}
	pixel := source.At(x, y)
	if c, ok := pixel.(color.NRGBA); ok {
		// Keep even the original RGB values of fully transparent PNG pixels.
		return color.NRGBA64{R: uint16(c.R) * 257, G: uint16(c.G) * 257, B: uint16(c.B) * 257, A: uint16(c.A) * 257}
	}
	return color.NRGBA64Model.Convert(pixel)
}
