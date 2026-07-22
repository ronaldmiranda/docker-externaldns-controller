// Package controller runs the reconcile loop: it reads desired endpoints from a
// source, compares them against what the registry reports, and applies the
// resulting plan to the provider.
package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
	"github.com/ronaldmiranda/externaldns-controller/internal/source"
)

// Registry is the ownership-aware view of a provider the controller reconciles
// against. *registry.TXTRegistry satisfies it.
type Registry interface {
	Records(ctx context.Context) ([]*endpoint.Endpoint, error)
	ApplyChanges(ctx context.Context, changes *plan.Changes) error
}

// Options configures a Controller.
type Options struct {
	OwnerID          string
	ResyncInterval   time.Duration
	DebounceInterval time.Duration
	DryRun           bool
}

// Controller ties a source and a registry together.
type Controller struct {
	source   source.Source
	registry Registry
	opts     Options
	log      *slog.Logger
}

// New builds a Controller.
func New(src source.Source, reg Registry, opts Options, log *slog.Logger) *Controller {
	return &Controller{source: src, registry: reg, opts: opts, log: log}
}

// Run blocks, reconciling on source events and on a periodic resync ticker,
// until ctx is cancelled. The first reconcile runs synchronously so
// misconfiguration (bad credentials, unreachable daemon) fails fast.
func (c *Controller) Run(ctx context.Context) error {
	events := c.source.Watch(ctx, c.opts.DebounceInterval)
	ticker := time.NewTicker(c.opts.ResyncInterval)
	defer ticker.Stop()

	c.log.Info("starting reconcile loop",
		"owner_id", c.opts.OwnerID,
		"resync_interval", c.opts.ResyncInterval,
		"dry_run", c.opts.DryRun)

	if err := c.reconcile(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-events:
			if !ok {
				return ctx.Err()
			}
			if err := c.reconcile(ctx); err != nil {
				c.log.Error("reconcile failed", "error", err)
			}
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				c.log.Error("resync failed", "error", err)
			}
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) error {
	start := time.Now()

	desired, err := c.source.Endpoints(ctx)
	if err != nil {
		return err
	}
	current, err := c.registry.Records(ctx)
	if err != nil {
		return err
	}

	p := (&plan.Plan{
		Current: current,
		Desired: desired,
		OwnerID: c.opts.OwnerID,
	}).Calculate()

	for _, conflict := range p.Conflicts {
		c.log.Warn("record already exists and is not managed by this controller, skipping",
			"name", conflict.DNSName, "type", conflict.RecordType)
	}

	if p.Changes.Empty() {
		c.log.Debug("all records up to date", "desired", len(desired), "took", time.Since(start).Round(time.Millisecond))
		return nil
	}

	c.logChanges(p.Changes)

	if c.opts.DryRun {
		c.log.Info("dry-run: no changes applied")
		return nil
	}

	if err := c.registry.ApplyChanges(ctx, p.Changes); err != nil {
		return err
	}
	c.log.Info("reconcile applied",
		"created", len(p.Changes.Create),
		"updated", len(p.Changes.UpdateNew),
		"deleted", len(p.Changes.Delete),
		"took", time.Since(start).Round(time.Millisecond))
	return nil
}

func (c *Controller) logChanges(changes *plan.Changes) {
	for _, ep := range changes.Create {
		c.log.Info("create", "name", ep.DNSName, "type", ep.RecordType, "targets", ep.Targets)
	}
	for _, ep := range changes.UpdateNew {
		c.log.Info("update", "name", ep.DNSName, "type", ep.RecordType, "targets", ep.Targets)
	}
	for _, ep := range changes.Delete {
		c.log.Info("delete", "name", ep.DNSName, "type", ep.RecordType, "targets", ep.Targets)
	}
}
