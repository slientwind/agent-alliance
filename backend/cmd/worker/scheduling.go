package main

import (
	"context"
	"sync"

	"alliance/internal/automations"
	"alliance/internal/ecosystem"
	"alliance/internal/monitor"
	"alliance/internal/resources"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
)

type scheduleRegistry interface {
	RegisterWorkflowWithOptions(interface{}, workflow.RegisterOptions)
	RegisterActivityWithOptions(interface{}, activity.RegisterOptions)
}

func registerScheduledWorkflows(w scheduleRegistry, scheduled automations.Activities, discovery ecosystem.Activities) {
	w.RegisterWorkflowWithOptions(automations.AutomationWorkflow, workflow.RegisterOptions{Name: automations.WorkflowName})
	w.RegisterActivityWithOptions(scheduled.Prepare, activity.RegisterOptions{Name: automations.PrepareActivity})
	w.RegisterActivityWithOptions(scheduled.Progress, activity.RegisterOptions{Name: automations.ProgressActivity})
	w.RegisterWorkflowWithOptions(ecosystem.EcosystemScanWorkflow, workflow.RegisterOptions{Name: ecosystem.ScanWorkflowName})
	w.RegisterActivityWithOptions(discovery.Scan, activity.RegisterOptions{Name: ecosystem.ScanActivityName})
}

func runScheduledJobs(ctx context.Context, catalog resources.Catalog, temporal client.Client, queue string) func() {
	return startBackgroundJobs(ctx,
		func(ctx context.Context) { automations.Serve(ctx, catalog.DB, temporal.ScheduleClient(), queue) },
		func(ctx context.Context) { ecosystem.Serve(ctx, catalog.DB, temporal, queue) },
		func(ctx context.Context) { monitor.ServeRollups(ctx, catalog.DB) },
		(monitor.Alerts{DB: catalog.DB, Vault: catalog.Vault}).Serve,
	)
}

func startBackgroundJobs(ctx context.Context, tasks ...func(context.Context)) func() {
	ctx, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for _, task := range tasks {
		workers.Go(func() { task(ctx) })
	}
	return func() {
		stop()
		workers.Wait()
	}
}
