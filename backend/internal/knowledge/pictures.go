package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image/jpeg"
	"io"
	"path/filepath"
	"strings"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const PictureLimit = 3 << 20
const RunPictureLimit = 6 << 20
const MaxPictures = 4

type PictureUpload struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type Picture struct {
	Name, Media string
	Version     int
	Data        []byte
}

func PictureMedia(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return ""
}

func (p PictureUpload) Validate() (Picture, error) {
	media := PictureMedia(p.Name)
	if media == "" || len(p.Name) > 250 || strings.ContainsAny(p.Name, "/\\\r\n\x00") {
		return Picture{}, web.Fail(422, "请选择 PNG 或 JPEG 图片")
	}
	if len(p.Data) == 0 || len(p.Data) > PictureLimit {
		return Picture{}, web.Fail(422, "图片须非空且不超过 3 MiB")
	}
	if media == "image/png" {
		if _, _, err := PNGDimensions(bytes.NewReader(p.Data)); err != nil {
			return Picture{}, web.Fail(422, err.Error())
		}
	} else {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(p.Data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 4194304 {
			return Picture{}, web.Fail(422, "JPEG 无效或超过 4194304 像素")
		}
		if _, err := jpeg.Decode(bytes.NewReader(p.Data)); err != nil {
			return Picture{}, web.Fail(422, "JPEG 图像数据损坏")
		}
	}
	return Picture{Name: p.Name, Media: media, Version: 1, Data: p.Data}, nil
}

func (o Objects) NewPictureSource(ctx context.Context, tx pgx.Tx, project, actor string, p Picture) (string, error) {
	return o.newReadableSource(ctx, tx, project, actor, p.Name, bytes.NewReader(p.Data), PictureLimit, p.Media, extraction{})
}

func RunPictures(ctx context.Context, tx pgx.Tx, run string) ([]Picture, error) {
	rows, err := tx.Query(ctx, `select f.filename,f.version,f.picture_media,coalesce(f.picture_hash,''),coalesce(o.size_bytes,0)
 from run_source_inputs f left join objects o on o.hash=f.picture_hash
 where f.run_id=$1 and f.picture_media is not null order by f.picture_order nulls last,f.source_id`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type original struct {
		name, media, hash string
		version           int
		size              int64
	}
	var originals []original
	var total int64
	for rows.Next() {
		var file original
		if err := rows.Scan(&file.name, &file.version, &file.media, &file.hash, &file.size); err != nil {
			return nil, err
		}
		if file.hash == "" || file.size < 1 || file.size > PictureLimit {
			return nil, web.Fail(409, "已选择的图片失效或无法读取")
		}
		originals = append(originals, file)
		total += file.size
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(originals) == 0 || len(originals) > MaxPictures || total > RunPictureLimit {
		return nil, web.Fail(409, "请引用 1 到 4 张 PNG 或 JPEG 图片，合计不超过 6 MiB")
	}
	store := ConfiguredObjects()
	pictures := make([]Picture, 0, len(originals))
	for _, original := range originals {
		file, err := store.Open(original.hash)
		if err != nil {
			return nil, web.Fail(409, "图片原件暂不可用")
		}
		data, err := io.ReadAll(io.LimitReader(file, PictureLimit+1))
		file.Close()
		if err != nil || int64(len(data)) != original.size || fmt.Sprintf("%x", sha256.Sum256(data)) != original.hash {
			return nil, web.Fail(409, "图片原件无法通过完整性检查")
		}
		pictures = append(pictures, Picture{Name: original.name, Version: original.version, Media: original.media, Data: data})
	}
	return pictures, nil
}
