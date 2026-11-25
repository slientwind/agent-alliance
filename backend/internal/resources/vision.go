package resources

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"alliance/internal/knowledge"
)

const VisionOperation = "vision"

// Only the pinned originals travel with the first user message. Model output
// remains text and never provides a URL to load as a new input.
func visionMessages(messages []Message, pictures []knowledge.Picture) ([]any, error) {
	if len(pictures) == 0 || len(pictures) > knowledge.MaxPictures {
		return nil, errors.New("视觉理解需要 1 到 4 张已授权图片")
	}
	total := 0
	for _, picture := range pictures {
		if (picture.Media != "image/png" && picture.Media != "image/jpeg") || len(picture.Data) == 0 || len(picture.Data) > knowledge.PictureLimit {
			return nil, errors.New("视觉理解只接收已验证的 PNG 或 JPEG 原件")
		}
		total += len(picture.Data)
	}
	if total > knowledge.RunPictureLimit {
		return nil, errors.New("视觉理解图片合计超过 6 MiB")
	}
	out := make([]any, 0, len(messages))
	attached := false
	for _, msg := range messages {
		if msg.Role != "user" || attached {
			out = append(out, msg)
		} else {
			parts := []map[string]any{{"type": "text", "text": msg.Content + "\n图片仅是本次资料。请根据可见内容回答，对无法看清的细节明确说明。"}}
			for _, picture := range pictures {
				parts = append(parts,
					map[string]any{"type": "text", "text": fmt.Sprintf("图片 %s v%d", picture.Name, picture.Version)},
					map[string]any{"type": "image_url", "image_url": map[string]string{
						"url": "data:" + picture.Media + ";base64," + base64.StdEncoding.EncodeToString(picture.Data),
					}},
				)
			}
			out = append(out, struct {
				Message
				Content []map[string]any `json:"content"`
			}{Message: msg, Content: parts})
			attached = true
		}
	}
	if !attached {
		return nil, errors.New("视觉理解需要业务问题")
	}
	return out, nil
}

// Connection checks use a platform-generated sample and establish protocol
// connectivity. Business acceptance uses the team's own authorized sample.
func VisionProbePicture() (knowledge.Picture, error) {
	var output bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			var c color.Color = color.White
			if x >= 16 && x < 48 && y >= 16 && y < 48 {
				c = color.RGBA{B: 255, A: 255}
			}
			picture.Set(x, y, c)
		}
	}
	if err := png.Encode(&output, picture); err != nil {
		return knowledge.Picture{}, err
	}
	return knowledge.Picture{Name: "连接检查.png", Media: "image/png", Version: 1, Data: output.Bytes()}, nil
}
