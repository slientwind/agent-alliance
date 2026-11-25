package main

import (
	"context"
	"reflect"
	"testing"

	"alliance/internal/automations"
	"alliance/internal/ecosystem"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

type recordingRegistry struct{ workflows, activities []string }

func (r *recordingRegistry) RegisterWorkflowWithOptions(_ interface{}, options workflow.RegisterOptions) {
	r.workflows = append(r.workflows, options.Name)
}

func (r *recordingRegistry) RegisterActivityWithOptions(_ interface{}, options activity.RegisterOptions) {
	r.activities = append(r.activities, options.Name)
}

func TestScheduledWorkflowsUseEstablishedTemporalNames(t *testing.T) {
	var registry recordingRegistry
	registerScheduledWorkflows(&registry, automations.Activities{}, ecosystem.Activities{})
	if !reflect.DeepEqual(registry.workflows, []string{automations.WorkflowName, ecosystem.ScanWorkflowName}) ||
		!reflect.DeepEqual(registry.activities, []string{automations.PrepareActivity, automations.ProgressActivity, ecosystem.ScanActivityName}) {
		t.Fatalf("scheduled worker registration changed: %+v", registry)
	}
}

func TestScheduledJobsFinishBeforeTheirClientsClose(t *testing.T) {
	released := make(chan struct{}, 4)
	job := func(ctx context.Context) {
		<-ctx.Done()
		released <- struct{}{}
	}
	stop := startBackgroundJobs(t.Context(), job, job, job, job)
	stop()
	if len(released) != cap(released) {
		t.Fatalf("only %d jobs shut down", len(released))
	}
}
