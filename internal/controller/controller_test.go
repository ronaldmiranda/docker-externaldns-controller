package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

type fakeSource struct {
	endpoints []*endpoint.Endpoint
	err       error
}

func (f *fakeSource) Endpoints(context.Context) ([]*endpoint.Endpoint, error) {
	return f.endpoints, f.err
}
func (f *fakeSource) Watch(context.Context, time.Duration) <-chan struct{} {
	return make(chan struct{})
}
func (f *fakeSource) Close() error { return nil }

type fakeRegistry struct {
	current   []*endpoint.Endpoint
	recordErr error
	applyErr  error
	applied   int
}

func (f *fakeRegistry) Records(context.Context) ([]*endpoint.Endpoint, error) {
	return f.current, f.recordErr
}
func (f *fakeRegistry) ApplyChanges(context.Context, *plan.Changes) error {
	f.applied++
	return f.applyErr
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newController(src *fakeSource, reg *fakeRegistry) *Controller {
	return New(src, reg, Options{OwnerID: "host-a"}, quietLogger())
}

// A provider change failing must not surface as an error from reconcile, so the
// controller keeps running (and the first, synchronous reconcile never exits
// the process).
func TestReconcileApplyFailureIsNotFatal(t *testing.T) {
	src := &fakeSource{endpoints: []*endpoint.Endpoint{
		endpoint.New("app.example.com", "A", 0, "203.0.113.10"),
	}}
	reg := &fakeRegistry{applyErr: errors.New("provider exploded")}
	c := newController(src, reg)

	if err := c.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile should swallow apply errors, got %v", err)
	}
	if reg.applied != 1 {
		t.Errorf("expected ApplyChanges to be attempted once, got %d", reg.applied)
	}
}

// Failing to read state, by contrast, is reported so the first reconcile can
// fail fast on misconfiguration.
func TestReconcileReadFailurePropagates(t *testing.T) {
	src := &fakeSource{err: errors.New("docker down")}
	reg := &fakeRegistry{}
	c := newController(src, reg)

	if err := c.reconcile(context.Background()); err == nil {
		t.Fatal("expected source read error to propagate")
	}

	src = &fakeSource{}
	reg = &fakeRegistry{recordErr: errors.New("bad credentials")}
	c = newController(src, reg)
	if err := c.reconcile(context.Background()); err == nil {
		t.Fatal("expected registry read error to propagate")
	}
}

// A clean reconcile that applies changes returns nil and applies exactly once.
func TestReconcileAppliesChanges(t *testing.T) {
	src := &fakeSource{endpoints: []*endpoint.Endpoint{
		endpoint.New("app.example.com", "A", 0, "203.0.113.10"),
	}}
	reg := &fakeRegistry{}
	c := newController(src, reg)

	if err := c.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reg.applied != 1 {
		t.Errorf("expected one apply, got %d", reg.applied)
	}
}
