package main

import (
	"alliance/internal/agentloop"
	"alliance/internal/app"
	"alliance/internal/automations"
	"alliance/internal/ecosystem"
	"alliance/internal/egress"
	"alliance/internal/hotstream"
	"alliance/internal/intake"
	"alliance/internal/knowledge"
	"alliance/internal/knowledgesync"
	"alliance/internal/mcphub"
	"alliance/internal/policy"
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
	var projectStream hotstream.Publisher
	if address := os.Getenv("HOT_STREAM_REDIS_URL"); address != "" {
		stream, err := hotstream.OpenRedis(address, catalog.Vault)
		if err != nil {
			log.Fatal(err)
		}
		defer stream.Close()
		projectStream = stream
	} else if address := os.Getenv("HOT_STREAM_PUBLISH_URL"); address != "" {
		projectStream, err = hotstream.OpenRelay(address, os.Getenv("CONNECTOR_TOKEN"))
		if err != nil {
			log.Fatal(err)
		}
	}
	a := runtime.Activities{Catalog: catalog, Hot: projectStream}
	w := worker.New(c, runtime.Queue(), worker.Options{})
	// Poll historical histories that already started with the old activity names.
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
	gateway := app.ModelGateway(catalog)
	general := agentloop.Activities{Planned: &a, Intake: intake.Server{DB: catalog.DB, Vault: catalog.Vault, Gateway: gateway}, DB: catalog.DB, Vault: catalog.Vault, Objects: knowledge.ConfiguredObjects(), Gateway: gateway, Hot: projectStream}
	general.MCP = &mcphub.Runtime{DB: catalog.DB, Client: mcphub.Client{Vault: catalog.Vault, Egress: egress.Policy{DB: catalog.DB}}}
	general.Search = &websearch.Runtime{DB: catalog.DB, Client: websearch.Client{Vault: catalog.Vault, Egress: egress.Policy{DB: catalog.DB}}}
	generalQueue := runtime.GeneralQueue()
	generalWorker := worker.New(c, generalQueue, worker.Options{})
	generalWorker.RegisterWorkflowWithOptions(agentloop.GeneralAgentWorkflow, workflow.RegisterOptions{Name: agentloop.WorkflowName})
	generalWorker.RegisterActivityWithOptions(general.AdvanceTurn, activity.RegisterOptions{Name: agentloop.AdvanceTurnActivity})
	generalWorker.RegisterActivityWithOptions(general.ExecuteTool, activity.RegisterOptions{Name: agentloop.ExecuteToolActivity})
	scheduled := automations.Activities{DB: catalog.DB, Vault: catalog.Vault}
	discovery := ecosystem.Activities{DB: catalog.DB, Vault: catalog.Vault, InspectorURL: os.Getenv("ECOSYSTEM_INSPECTOR_URL"),
		InspectorToken: os.Getenv("CONNECTOR_TOKEN")}
	registerScheduledWorkflows(generalWorker, scheduled, discovery)
	if err := generalWorker.Start(); err != nil {
		log.Fatal(err)
	}
	defer generalWorker.Stop()
	stopScheduled := runScheduledJobs(ctx, catalog, c, generalQueue)
	defer stopScheduled()
	go general.Dispatch(ctx, c, generalQueue)
	stopMemory := startBackgroundJobs(ctx, general.RefreshConversationMemory)
	defer stopMemory()
	go a.FollowRemote(ctx, c)
	go a.CleanRetention(ctx)
	go knowledge.MaintainPassages(ctx, catalog.DB)
	go (knowledgesync.Server{DB: catalog.DB, Vault: catalog.Vault, Objects: knowledge.ConfiguredObjects(),
		Egress: egress.Policy{DB: catalog.DB}}).Serve(ctx)
	go knowledge.ConfiguredObjects().ServeArtifactPreviews(ctx, catalog.DB)
	go telemetry.Serve(ctx, catalog.DB)
	go policy.PruneDLPExchanges(ctx, catalog.DB)
	a.Dispatch(ctx, c)
}
