package knowledge

import (
	"errors"
	"image/png"
	"io"
)

// Read the dimensions before allocating pixel memory. Decode the full PNG to check
// that truncated images never become evidence or deliverable files.
func PNGDimensions(input io.ReadSeeker) (int, int, error) {
	cfg, err := png.DecodeConfig(input)
	if err != nil || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 4194304 {
		return 0, 0, errors.New("PNG 无效或超过 4194304 像素")
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	if _, err = png.Decode(input); err != nil {
		return 0, 0, errors.New("PNG 图像数据损坏")
	}
	_, err = input.Seek(0, io.SeekStart)
	return cfg.Width, cfg.Height, err
}
