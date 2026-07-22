// Package config holds the controller's runtime configuration and the helpers
// used to source defaults from environment variables. Flags (defined in cmd)
// take precedence over the environment, and the environment over the built-in
// defaults.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Provider selects the DNS backend, e.g. "cloudflare".
	Provider string

	// LabelPrefix is the container label namespace, e.g. "externaldns"
	// -> externaldns.hostname, externaldns.target, ...
	LabelPrefix string

	// OwnerID identifies this controller instance in ownership TXT records.
	// Records owned by a different id are never modified.
	OwnerID string

	// Registry selects how ownership is tracked: "txt" (companion TXT records)
	// or "noop" (manage everything in scope). Empty means "pick the default for
	// the provider".
	Registry string

	// TXTPrefix is prepended to ownership TXT record names.
	TXTPrefix string

	// DomainFilter, when set, restricts management to these zones/domains.
	DomainFilter []string

	// DefaultTarget is used as the content of address records whose container
	// omits an explicit target (typically the Docker host's public IP).
	DefaultTarget string

	// EnableTraefik turns hostnames declared in Traefik router rule labels
	// (Host(...)/HostSNI(...)) into managed records.
	EnableTraefik bool

	// ResyncInterval is how often a full reconcile runs without an event.
	ResyncInterval time.Duration

	// DebounceInterval batches bursts of container events into one reconcile.
	DebounceInterval time.Duration

	// DryRun logs the changes a reconcile would make without applying them.
	DryRun bool

	// LogLevel is "info" (default) or "debug".
	LogLevel string

	// Cloudflare-specific.
	CloudflareAPIToken string

	// Pi-hole-specific.
	PiholeURL         string
	PiholePassword    string
	PiholeTLSInsecure bool
}

// Validate checks required fields for the selected provider.
func (c *Config) Validate() error {
	if c.OwnerID == "" {
		return fmt.Errorf("owner id must not be empty")
	}
	switch c.Provider {
	case "cloudflare":
		if c.CloudflareAPIToken == "" {
			return fmt.Errorf("provider cloudflare requires CLOUDFLARE_API_TOKEN (or --cf-api-token)")
		}
	case "pihole":
		if c.PiholeURL == "" {
			return fmt.Errorf("provider pihole requires PIHOLE_URL (or --pihole-url)")
		}
		if c.PiholePassword == "" {
			return fmt.Errorf("provider pihole requires PIHOLE_PASSWORD (or --pihole-password)")
		}
	case "":
		return fmt.Errorf("no provider selected")
	default:
		return fmt.Errorf("unknown provider %q", c.Provider)
	}

	switch c.Registry {
	case "", "txt", "noop":
	default:
		return fmt.Errorf("unknown registry %q (want txt or noop)", c.Registry)
	}
	return nil
}

// RegistryKind resolves the registry to use, defaulting per provider when the
// user did not pick one. Providers that cannot store TXT records default to the
// noop registry.
func (c *Config) RegistryKind() string {
	if c.Registry != "" {
		return c.Registry
	}
	if c.Provider == "pihole" {
		return "noop"
	}
	return "txt"
}

// EnvOr returns the environment variable value for key, or def if unset/empty.
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvBool interprets an environment variable as a boolean, returning def when
// unset. Any value other than "false"/"0" counts as true.
func EnvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v != "false" && v != "0"
}

// EnvDuration parses an environment variable as a duration, returning def when
// unset and an error when malformed.
func EnvDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, v, err)
	}
	return d, nil
}

// SplitList parses a comma-separated environment value into a trimmed slice.
func SplitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// DefaultOwnerID returns a stable per-host owner id: the machine hostname, or
// "externaldns-controller" if it cannot be determined.
func DefaultOwnerID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "externaldns-controller"
}
