package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"alliance/internal/ecosysteminspect"
)

func main() {
	token := os.Getenv("CONNECTOR_TOKEN")
	if token == "" {
		log.Fatal("CONNECTOR_TOKEN is required")
	}
	server := &http.Server{
		Addr: ":18828", Handler: ecosysteminspect.Handler(token),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 8 * time.Second, WriteTimeout: 8 * time.Second,
		IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10,
	}
	log.Fatal(server.ListenAndServe())
}
