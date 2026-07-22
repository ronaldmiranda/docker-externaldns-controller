// Command externaldns-controller watches container labels and reconciles the
// declared DNS records into a cloud provider. The provider is selected at
// runtime, so the same binary serves Cloudflare today and any future backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ronaldmiranda/externaldns-controller/internal/config"
	"github.com/ronaldmiranda/externaldns-controller/internal/controller"
	"github.com/ronaldmiranda/externaldns-controller/internal/provider"
	"github.com/ronaldmiranda/externaldns-controller/internal/provider/cloudflare"
	"github.com/ronaldmiranda/externaldns-controller/internal/provider/pihole"
	"github.com/ronaldmiranda/externaldns-controller/internal/registry"
	dockersource "github.com/ronaldmiranda/externaldns-controller/internal/source/docker"
)

func main() {
	cmd, err := newRootCommand()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newRootCommand builds the cobra command with flag defaults sourced from the
// environment. It returns an error if an environment default is malformed.
func newRootCommand() (*cobra.Command, error) {
	cfg := &config.Config{}

	resyncDefault, err := config.EnvDuration("RESYNC_INTERVAL", 5*time.Minute)
	if err != nil {
		return nil, err
	}
	debounceDefault, err := config.EnvDuration("DEBOUNCE_INTERVAL", 2*time.Second)
	if err != nil {
		return nil, err
	}

	cmd := &cobra.Command{
		Use:   "externaldns-controller",
		Short: "Manage cloud DNS records from Docker container labels",
		Long: "externaldns-controller watches Docker containers and keeps a cloud DNS\n" +
			"provider in sync with the records declared in their labels. Inspired by\n" +
			"external-dns, it tracks ownership with companion TXT records so it only\n" +
			"ever touches the records it created.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Provider, "provider", config.EnvOr("PROVIDER", "cloudflare"),
		"DNS provider to manage (cloudflare, pihole)")
	f.StringVar(&cfg.Registry, "registry", config.EnvOr("REGISTRY", ""),
		"ownership tracking: txt or noop (default: per-provider)")
	f.StringVar(&cfg.LabelPrefix, "label-prefix", config.EnvOr("LABEL_PREFIX", "externaldns"),
		"container label namespace to read")
	f.StringVar(&cfg.OwnerID, "owner-id", config.EnvOr("OWNER_ID", config.DefaultOwnerID()),
		"identity recorded in ownership TXT records")
	f.StringVar(&cfg.TXTPrefix, "txt-prefix", config.EnvOr("TXT_PREFIX", "externaldns-"),
		"prefix for ownership TXT record names")
	f.StringSliceVar(&cfg.DomainFilter, "domain-filter", config.SplitList(os.Getenv("DOMAIN_FILTER")),
		"limit management to these domains/zones (comma-separated; default: all visible)")
	f.StringVar(&cfg.DefaultTarget, "default-target", config.EnvOr("DEFAULT_TARGET", ""),
		"fallback content for address records without an explicit target")
	f.BoolVar(&cfg.EnableTraefik, "traefik", config.EnvBool("ENABLE_TRAEFIK", true),
		"also derive records from Traefik router Host(...) labels")
	f.DurationVar(&cfg.ResyncInterval, "resync-interval", resyncDefault,
		"how often to reconcile without an event")
	f.DurationVar(&cfg.DebounceInterval, "debounce-interval", debounceDefault,
		"how long to batch bursts of container events")
	f.BoolVar(&cfg.DryRun, "dry-run", config.EnvBool("DRY_RUN", false),
		"log changes without applying them")
	f.StringVar(&cfg.LogLevel, "log-level", config.EnvOr("LOG_LEVEL", "info"),
		"log level (info, debug)")
	f.StringVar(&cfg.CloudflareAPIToken, "cf-api-token", os.Getenv("CLOUDFLARE_API_TOKEN"),
		"Cloudflare API token (Zone > DNS > Edit)")
	f.StringVar(&cfg.PiholeURL, "pihole-url", config.EnvOr("PIHOLE_URL", ""),
		"Pi-hole base URL, e.g. http://pi.hole")
	f.StringVar(&cfg.PiholePassword, "pihole-password", os.Getenv("PIHOLE_PASSWORD"),
		"Pi-hole web/API password")
	f.BoolVar(&cfg.PiholeTLSInsecure, "pihole-tls-insecure", config.EnvBool("PIHOLE_TLS_INSECURE", false),
		"skip TLS verification for the Pi-hole API (self-signed certs)")

	return cmd, nil
}

func run(ctx context.Context, cfg *config.Config) error {
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	if err := cfg.Validate(); err != nil {
		return err
	}

	prov, err := newProvider(cfg, log)
	if err != nil {
		return err
	}
	reg, err := newRegistry(cfg, prov)
	if err != nil {
		return err
	}

	src, err := dockersource.New(cfg.LabelPrefix, cfg.DefaultTarget, cfg.EnableTraefik, log)
	if err != nil {
		return fmt.Errorf("docker source: %w", err)
	}
	defer src.Close()

	ctrl := controller.New(src, reg, controller.Options{
		OwnerID:          cfg.OwnerID,
		ResyncInterval:   cfg.ResyncInterval,
		DebounceInterval: cfg.DebounceInterval,
		DryRun:           cfg.DryRun,
	}, log)

	log.Info("externaldns-controller starting",
		"provider", cfg.Provider,
		"registry", cfg.RegistryKind(),
		"label_prefix", cfg.LabelPrefix,
		"traefik", cfg.EnableTraefik,
		"domain_filter", cfg.DomainFilter)

	if err := ctrl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// newProvider is the provider factory: extend the switch to add backends.
func newProvider(cfg *config.Config, log *slog.Logger) (provider.Provider, error) {
	switch cfg.Provider {
	case "cloudflare":
		return cloudflare.New(cfg.CloudflareAPIToken, cfg.DomainFilter, log), nil
	case "pihole":
		return pihole.New(cfg.PiholeURL, cfg.PiholePassword, cfg.PiholeTLSInsecure, cfg.DomainFilter, log)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

// newRegistry selects the ownership strategy for the provider.
func newRegistry(cfg *config.Config, prov provider.Provider) (controller.Registry, error) {
	switch cfg.RegistryKind() {
	case "txt":
		return registry.New(prov, cfg.OwnerID, cfg.TXTPrefix), nil
	case "noop":
		return registry.NewNoop(prov, cfg.OwnerID), nil
	default:
		return nil, fmt.Errorf("unknown registry %q", cfg.RegistryKind())
	}
}

func newLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	if level == "debug" {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
