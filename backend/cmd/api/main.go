package main

import (
	"alliance/internal/app"
	"alliance/internal/billing"
	"alliance/internal/channels"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/monitor"
	"alliance/internal/onboarding"
	"alliance/internal/planner"
	"alliance/internal/projects"
	"alliance/internal/services"
	"alliance/internal/web"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := app.Catalog(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer c.DB.Close()
	sso, err := identity.ConfiguredSSO()
	if err != nil {
		log.Fatal(err)
	}
	s := identity.Server{SSO: sso, DB: c.DB, Bootstrap: os.Getenv("BOOTSTRAP_TOKEN"), Secure: os.Getenv("COOKIE_SECURE") != "false"}
	if err := s.ActivateLoginPolicy(ctx); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	feishu := channels.NewFeishu(c.DB)
	feishu.Routes(mux, s)
	go feishu.Notify(ctx)
	go feishu.ImportAttachments(ctx)
	go feishu.ReviewMonitor(ctx)
	billing.Server{DB: c.DB}.Routes(mux, s)
	monitor.Server{DB: c.DB}.Routes(mux, s)
	services.Server{DB: c.DB, Planner: planner.Server{Catalog: c}}.Routes(mux, s)
	knowledge.Server{DB: c.DB, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	c.Routes(mux, s)
	onboarding.Server{Catalog: c, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	planner.Server{Catalog: c}.Routes(mux, s)
	projects.Server{DB: c.DB, Vault: c.Vault}.Routes(mux, s)
	mux.Handle("GET /api/health", web.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if err := c.DB.Ping(r.Context()); err != nil {
			return web.Fail(503, "数据库不可用")
		}
		return web.JSON(w, 200, map[string]string{"status": "ready"})
	}))
	if root := os.Getenv("WEB_ROOT"); root != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(root)))
	}
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18085"
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
		mux.ServeHTTP(w, r)
	})
	server := http.Server{Addr: addr, Handler: http.NewCrossOriginProtection().Handler(handler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(end)
	}()
	log.Print("API listening on ", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
