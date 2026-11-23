package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// ImageRegion uses the original PNG pixel grid, with an exclusive upper bound.
type ImageRegion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (r ImageRegion) Within(width, height int) bool {
	return r.X >= 0 && r.Y >= 0 && r.Width > 0 && r.Height > 0 &&
		r.X < width && r.Y < height && r.Width <= width-r.X && r.Height <= height-r.Y &&
		(r.Width != width || r.Height != height)
}

func (r ImageRegion) Bounds() image.Rectangle {
	return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
}

// EditedArtifactPicture returns the exact authorized base artifact by version.
func EditedArtifactPicture(ctx context.Context, tx pgx.Tx, artifact, actor string, version int) (Picture, error) {
	var p Picture
	var hash string
	err := tx.QueryRow(ctx, `select filename,object_hash from artifact_files
 where artifact_id=$1 and version=$2 and media_type='image/png' and distribution_readable($1,$2,$3)`, artifact, version, actor).
		Scan(&p.Name, &hash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return p, web.Fail(409, "基准 PNG 原件不可用或当前无权读取")
		}
		return p, err
	}
	file, err := ConfiguredObjects().Open(hash)
	if err != nil {
		return p, web.Fail(409, "基准图片原件暂不可用")
	}
	defer file.Close()
	const limit = 8 << 20
	p.Data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(p.Data) > limit || fmt.Sprintf("%x", sha256.Sum256(p.Data)) != hash {
		return Picture{}, web.Fail(409, "基准图无法通过完整性检查")
	}
	p.Media, p.Version = "image/png", version
	return p, nil
}

func (r ImageRegion) ValidateBase(p Picture) (image.Config, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(p.Data))
	if err != nil || !r.Within(cfg.Width, cfg.Height) {
		return cfg, web.Fail(422, "请在当前基准图内选择一个局部矩形")
	}
	w, h := int64(cfg.Width), int64(cfg.Height)
	if w*h < 512*512 || w*h > 2048*2048 || w > 8*h || h > 8*w {
		return cfg, web.Fail(422, "局部编辑要求基准图为 512² 至 2048² 像素，长短边比例最多 8:1")
	}
	return cfg, nil
}
