package main

import (
	"alliance/internal/app"
	"alliance/internal/knowledge"
	"alliance/internal/runtime"
	"alliance/internal/telemetry"
	"context"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	catalog, err := app.Catalog(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer catalog.DB.Close()
	c, err := client.Dial(client.Options{HostPort: os.Getenv("TEMPORAL_ADDR")})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	a := runtime.Activities{Catalog: catalog}
	w := worker.New(c, runtime.Queue(), worker.Options{})
	w.RegisterWorkflow(runtime.Collaborate)
	w.RegisterActivity(a.ExecuteStep)
	w.RegisterActivity(a.AdvanceTurn)
	w.RegisterActivity(a.ExecuteLookupAction)
	w.RegisterActivity(a.AdvanceExecutionTurn)
	w.RegisterActivity(a.AdvanceObservedTurn)
	w.RegisterActivity(a.ExecuteAction)
	w.RegisterActivity(a.CommitLocalObservation)
	w.RegisterActivity(a.Topology)
	w.RegisterActivity(a.EnableStepRemoteInput)
	w.RegisterActivity(a.EnableStepSourceReceipts)
	if err := w.Start(); err != nil {
		log.Fatal(err)
	}
	defer w.Stop()
	go a.FollowRemote(ctx, c)
	go a.CleanRetention(ctx)
	go knowledge.MaintainPassages(ctx, catalog.DB)
	go telemetry.Serve(ctx, catalog.DB)
	a.Dispatch(ctx, c)
}
