package ecosysteminspect

import (
	"crypto/subtle"
	"net/http"
	"time"

	"alliance/internal/web"
)

func Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /inspect", web.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			return web.Fail(403, "不能访问体检服务")
		}
		var input Input
		if err := web.ReadLimited(w, r, &input, RequestLimit); err != nil {
			return err
		}
		if len(input.Inventory) > 240 || len(input.Problems) > 8 || len(input.Description) > 16<<10 ||
			len(input.Metadata) > 64<<10 || len(input.License) > 32<<10 {
			return web.Fail(422, "扫描数据超过体检限制")
		}
		return web.JSON(w, http.StatusOK, Inspect(input, time.Now().UTC()))
	}))
	return mux
}
