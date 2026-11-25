package main

import (
	"alliance/internal/agentloop"
	"alliance/internal/app"
	"alliance/internal/automations"
	"alliance/internal/billing"
	"alliance/internal/channels"
	"alliance/internal/ecosystem"
	"alliance/internal/egress"
	"alliance/internal/glossary"
	"alliance/internal/hotstream"
	"alliance/internal/identity"
	"alliance/internal/intake"
	"alliance/internal/knowledge"
	"alliance/internal/knowledgesync"
	"alliance/internal/llmgateway"
	"alliance/internal/mailrelay"
	"alliance/internal/mcphub"
	"alliance/internal/monitor"
	"alliance/internal/onboarding"
	"alliance/internal/planner"
	"alliance/internal/policy"
	"alliance/internal/projects"
	"alliance/internal/sandbox"
	"alliance/internal/services"
	"alliance/internal/spaces"
	"alliance/internal/studio"
	"alliance/internal/web"
	"alliance/internal/websearch"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	spaces.Server{DB: c.DB, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	glossary.Server{DB: c.DB}.Routes(mux, s)
	bots := &channels.Bots{DB: c.DB, Vault: c.Vault}
	bots.Routes(ctx, mux, s)
	mail := mailrelay.Relay{DB: c.DB, Vault: c.Vault, Egress: egress.Policy{DB: c.DB}, Public: os.Getenv("PUBLIC_BASE_URL")}
	mail.Routes(mux, s)
	go (automations.Notifications{DB: c.DB, Feishu: bots.SendAutomationNotice, Email: mail.Send}).Serve(ctx)
	sandbox.Pool{DB: c.DB}.Routes(mux, s)
	knowledgesync.Server{DB: c.DB, Vault: c.Vault, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	billing.Server{DB: c.DB}.Routes(mux, s)
	monitor.Server{DB: c.DB}.Routes(mux, s)
	monitor.Alerts{DB: c.DB, Vault: c.Vault}.Routes(mux, s)
	egress.Admin{DB: c.DB}.Routes(mux, s)
	policy.DLPAdmin{DB: c.DB, Vault: c.Vault}.Routes(mux, s)
	llmgateway.Admin{DB: c.DB, Vault: c.Vault, Provider: llmgateway.HTTPProvider{Vault: c.Vault, Egress: &egress.Policy{DB: c.DB}}}.Routes(mux, s)
	mcphub.Admin{DB: c.DB, Client: mcphub.Client{Vault: c.Vault, Egress: egress.Policy{DB: c.DB}}}.Routes(mux, s)
	websearch.Server{DB: c.DB, Client: websearch.Client{Vault: c.Vault, Egress: egress.Policy{DB: c.DB}}}.Routes(mux, s)
	services.Server{DB: c.DB, Planner: planner.Server{Catalog: c}}.Routes(mux, s)
	knowledge.Server{DB: c.DB, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	c.Routes(mux, s)
	onboarding.Server{Catalog: c, Objects: knowledge.ConfiguredObjects()}.Routes(mux, s)
	planner.Server{Catalog: c}.Routes(mux, s)
	projectUpdates, err := projects.StartSessionUpdates(ctx, c.DB)
	if err != nil {
		log.Fatal(err)
	}
	var projectStream hotstream.HotStreamBus = hotstream.NewMemory()
	if address := os.Getenv("HOT_STREAM_REDIS_URL"); address != "" {
		stream, err := hotstream.OpenRedis(address, c.Vault)
		if err != nil {
			log.Fatal(err)
		}
		defer stream.Close()
		projectStream = stream
	} else {
		mux.Handle("POST "+hotstream.RelayPath, hotstream.RelayHandler(projectStream, os.Getenv("CONNECTOR_TOKEN")))
	}
	general := agentloop.Server{DB: c.DB, Vault: c.Vault}
	projects.Server{DB: c.DB, Vault: c.Vault, SessionUpdates: projectUpdates, Hot: projectStream, QueueAnchorEdit: general.QueueAnchorEdit,
		CommentConnections: bots.CommentConnections}.Routes(mux, s)
	general.Routes(mux, s)
	intake.Server{DB: c.DB, Vault: c.Vault}.Routes(mux, s)
	studio.Server{DB: c.DB, Vault: c.Vault, Gateway: app.ModelGateway(c)}.Routes(mux, s)
	ecosystem.Server{DB: c.DB, Vault: c.Vault}.Routes(mux, s)
	automations.Server{DB: c.DB, Vault: c.Vault, Feishu: bots}.Routes(mux, s)
	mux.Handle("GET /api/health", web.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if err := c.DB.Ping(r.Context()); err != nil {
			return web.Fail(503, "数据库不可用")
		}
		return web.JSON(w, 200, map[string]string{"status": "ready"})
	}))
	if root := os.Getenv("WEB_ROOT"); root != "" {
		mux.Handle("GET /", spaHandler(os.DirFS(root)))
	}
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18085"
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, requestBodyLimit(r))
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

func requestBodyLimit(r *http.Request) int64 {
	path, isAPI := strings.CutPrefix(r.URL.Path, "/api/")
	if !isAPI {
		return 9 << 20
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[1] == "" {
		return 9 << 20
	}
	if r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "studio" && parts[2] == "chat" {
		return studio.MaxChatBody
	}
	if parts[0] == "projects" && parts[2] == "sources" &&
		(r.Method == http.MethodPost && len(parts) == 3 || r.Method == http.MethodPut && len(parts) == 4 && parts[3] != "") {
		return knowledge.AttachmentLimit + 1
	}
	return 9 << 20
}
