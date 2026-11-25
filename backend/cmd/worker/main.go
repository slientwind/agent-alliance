package main

import (
	"alliance/internal/agentloop"
	"alliance/internal/app"
	"alliance/internal/automations"
	"alliance/internal/egress"
	"alliance/internal/knowledge"
	"alliance/internal/mcphub"
	"alliance/internal/monitor"
	"alliance/internal/runtime"
	"alliance/internal/telemetry"
	"alliance/internal/websearch"
	"context"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
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
	w.RegisterActivity(a.AdvanceTextStep)
	w.RegisterActivity(a.AdvanceEvidenceStep)
	w.RegisterActivity(a.AdvanceEvidenceQuestion)
	w.RegisterActivity(a.AdvanceFocusedEvidence)
	w.RegisterActivity(a.AdvanceRequirementEvidence)
	w.RegisterActivity(a.CommitTextReply)
	w.RegisterActivity(a.CommitEvidenceReply)
	w.RegisterActivity(a.CommitEvidenceQuestion)
	w.RegisterActivity(a.AdvanceTurn)
	w.RegisterActivity(a.ExecuteLookupAction)
	w.RegisterActivity(a.AdvanceExecutionTurn)
	w.RegisterActivity(a.AdvanceObservedTurn)
	w.RegisterActivity(a.AdvanceBatchedLookup)
	w.RegisterActivity(a.ExecuteAction)
	w.RegisterActivity(a.CommitLocalObservation)
	w.RegisterActivity(a.ExecuteEvidenceAction)
	w.RegisterActivity(a.CommitEvidenceObservation)
	w.RegisterActivity(a.Topology)
	w.RegisterActivity(a.EnableStepRemoteInput)
	w.RegisterActivity(a.EnableStepSourceReceipts)
	w.RegisterActivity(a.EnableStepToolChoices)
	w.RegisterActivity(a.EnableStepDispatchWaits)
	if err := w.Start(); err != nil {
		log.Fatal(err)
	}
	defer w.Stop()
	general := agentloop.Activities{DB: catalog.DB, Vault: catalog.Vault, Objects: knowledge.ConfiguredObjects(), Gateway: app.ModelGateway(catalog)}
	general.MCP = &mcphub.Runtime{DB: catalog.DB, Client: mcphub.Client{Vault: catalog.Vault, Egress: egress.Policy{DB: catalog.DB}}}
	general.Search = &websearch.Runtime{DB: catalog.DB, Client: websearch.Client{Vault: catalog.Vault, Egress: egress.Policy{DB: catalog.DB}}}
	generalQueue := runtime.Queue() + "-general"
	generalWorker := worker.New(c, generalQueue, worker.Options{})
	generalWorker.RegisterWorkflowWithOptions(agentloop.GeneralAgentWorkflow, workflow.RegisterOptions{Name: agentloop.WorkflowName})
	generalWorker.RegisterActivityWithOptions(general.AdvanceTurn, activity.RegisterOptions{Name: agentloop.AdvanceTurnActivity})
	generalWorker.RegisterActivityWithOptions(general.ExecuteTool, activity.RegisterOptions{Name: agentloop.ExecuteToolActivity})
	scheduled := automations.Activities{DB: catalog.DB, Vault: catalog.Vault}
	generalWorker.RegisterWorkflowWithOptions(automations.AutomationWorkflow, workflow.RegisterOptions{Name: automations.WorkflowName})
	generalWorker.RegisterActivityWithOptions(scheduled.Prepare, activity.RegisterOptions{Name: automations.PrepareActivity})
	generalWorker.RegisterActivityWithOptions(scheduled.Progress, activity.RegisterOptions{Name: automations.ProgressActivity})
	if err := generalWorker.Start(); err != nil {
		log.Fatal(err)
	}
	defer generalWorker.Stop()
	go general.Dispatch(ctx, c, generalQueue)
	go automations.Serve(ctx, catalog.DB, c.ScheduleClient(), generalQueue)
	go a.FollowRemote(ctx, c)
	go a.CleanRetention(ctx)
	go knowledge.MaintainPassages(ctx, catalog.DB)
	go knowledge.ConfiguredObjects().ServeArtifactPreviews(ctx, catalog.DB)
	go monitor.ServeRollups(ctx, catalog.DB)
	go (monitor.Alerts{DB: catalog.DB, Vault: catalog.Vault}).Serve(ctx)
	go telemetry.Serve(ctx, catalog.DB)
	a.Dispatch(ctx, c)
}
