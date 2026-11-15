package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func main() {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("ALLIANCE_MASTER_KEY"))
	if err != nil || len(key) != 32 {
		log.Fatal("ALLIANCE_MASTER_KEY must be base64 encoded 32 bytes")
	}
	var app http.Handler
	var shutdown func()
	if dsn := os.Getenv("ALLIANCE_DATABASE_URL"); dsn != "" {
		db, e := openDatabase(dsn, key)
		if e != nil {
			log.Fatal(e)
		}
		defer db.Close()
		mode := env("ALLIANCE_MODE", "api")
		if mode == "worker" {
			worker := newWorker(db, key, env("ALLIANCE_WORKER_ID", id("worker_")), os.Getenv("ALLIANCE_ALLOW_PRIVATE") == "true")
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			log.Printf("Worker %s started", worker.ID)
			worker.Run(ctx)
			return
		}
		if mode != "api" {
			log.Fatal("ALLIANCE_MODE must be api or worker")
		}
		gateway := newGateway(db, key, os.Getenv("ALLIANCE_ALLOW_PRIVATE") == "true")
		gateway.allowRegistration = os.Getenv("ALLIANCE_ALLOW_REGISTRATION") == "true"
		gateway.demo = os.Getenv("ALLIANCE_DEMO") == "true"
		gateway.secureCookies = os.Getenv("ALLIANCE_INSECURE_COOKIES") != "true"
		if path := os.Getenv("ALLIANCE_IMPORT_JSON"); path != "" {
			if !gateway.demo {
				log.Fatal("legacy import requires explicit demo workspace mode")
			}
			if e = gateway.importLegacy(context.Background(), path); e != nil {
				log.Fatal(e)
			}
		}
		app = gateway
		stopGRPC, e := gateway.startGRPC()
		if e != nil {
			log.Fatal(e)
		}
		shutdown = func() { stopGRPC(); gateway.shutdown() }
	} else {
		token := os.Getenv("ALLIANCE_API_KEY")
		if len(token) < 16 {
			log.Fatal("configure ALLIANCE_DATABASE_URL for production, or ALLIANCE_API_KEY for legacy API-only mode")
		}
		store, e := openStore(env("ALLIANCE_DATA", "data/state.json"), key)
		if e != nil {
			log.Fatal(e)
		}
		server := newServer(store, token, os.Getenv("ALLIANCE_ALLOW_PRIVATE") == "true")
		app = server
		shutdown = server.shutdown
	}
	static := env("ALLIANCE_STATIC", "../frontend/dist")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			app.ServeHTTP(w, r)
			return
		}
		path := filepath.Join(static, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(static, "index.html"))
			return
		}
		http.FileServer(http.Dir(static)).ServeHTTP(w, r)
	})
	server := &http.Server{Addr: env("ALLIANCE_ADDR", "127.0.0.1:8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("Agent Alliance API: http://%s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	shutdown()
}
