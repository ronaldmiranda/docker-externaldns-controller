// Package docker is a Source that discovers desired DNS endpoints from the
// labels of running Docker containers and watches the daemon for changes.
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

// Source reads endpoints from container labels sharing a common prefix.
type Source struct {
	cli           *client.Client
	labelPrefix   string
	defaultTarget string
	traefik       bool
	log           *slog.Logger
}

// New connects to the Docker daemon (honouring DOCKER_HOST and the usual
// environment) and returns a Source. defaultTarget is used as the content of
// address records whose container omits an explicit target; it may be empty.
// When traefik is true, hostnames declared in Traefik router rule labels are
// also turned into records.
func New(labelPrefix, defaultTarget string, traefik bool, log *slog.Logger) (*Source, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &Source{cli: cli, labelPrefix: labelPrefix, defaultTarget: defaultTarget, traefik: traefik, log: log}, nil
}

// Close releases the Docker client.
func (s *Source) Close() error { return s.cli.Close() }

// Endpoints lists running containers carrying the label prefix and parses their
// record declarations. A container with invalid labels is logged and skipped so
// one bad definition never blocks the others.
func (s *Source) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {
	f := make(client.Filters).Add("status", "running")
	res, err := s.cli.ContainerList(ctx, client.ContainerListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var endpoints []*endpoint.Endpoint
	seen := map[string]string{} // endpoint key -> container, for duplicate detection
	for _, ctr := range res.Items {
		name := containerName(ctr)
		for _, ep := range s.containerEndpoints(name, ctr.Labels) {
			key := ep.Key()
			if other, dup := seen[key]; dup {
				s.log.Error("duplicate DNS record, keeping first",
					"record", key, "kept", other, "ignored", name)
				continue
			}
			seen[key] = name
			endpoints = append(endpoints, ep)
		}
	}

	sort.Slice(endpoints, func(i, j int) bool {
		return endpoints[i].Key() < endpoints[j].Key()
	})
	return endpoints, nil
}

// containerEndpoints parses one container's labels through every enabled
// scheme. Native externaldns.* records come first so that, on a name clash, an
// explicit declaration wins over a Traefik-derived one. A parser error is
// logged and its output dropped without discarding the other parser's records.
func (s *Source) containerEndpoints(name string, labels map[string]string) []*endpoint.Endpoint {
	var out []*endpoint.Endpoint

	native, err := parseLabels(s.labelPrefix, s.defaultTarget, labels)
	if err != nil {
		s.log.Error("ignoring container with invalid externaldns labels", "container", name, "error", err)
	} else {
		out = append(out, native...)
	}

	if s.traefik {
		traefik, err := parseTraefikLabels(s.labelPrefix, s.defaultTarget, labels)
		if err != nil {
			s.log.Error("ignoring container with unresolvable traefik labels", "container", name, "error", err)
		} else {
			out = append(out, traefik...)
		}
	}
	return out
}

func containerName(ctr container.Summary) string {
	if len(ctr.Names) > 0 {
		return strings.TrimPrefix(ctr.Names[0], "/")
	}
	if len(ctr.ID) >= 12 {
		return ctr.ID[:12]
	}
	return ctr.ID
}
