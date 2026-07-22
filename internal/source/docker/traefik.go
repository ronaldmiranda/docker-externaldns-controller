package docker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

// Traefik expresses the public hostname of a service in its router rule labels,
// e.g.
//
//	traefik.http.routers.app.rule=Host(`app.example.com`)
//	traefik.tcp.routers.db.rule=HostSNI(`db.example.com`)
//
// parseTraefikLabels extracts those hostnames and turns each into a DNS record,
// so a stack already labelled for Traefik needs no extra externaldns labels to
// get DNS. Because Traefik labels say nothing about where the record should
// point, the record is shaped from the container's non-indexed externaldns.*
// keys (type/target/ttl/proxied) falling back to the controller's default
// target — typically the host's public IP where Traefik is exposed.
//
// Containers with traefik.enable=false are skipped, mirroring Traefik itself.
func parseTraefikLabels(prefix, defaultTarget string, labels map[string]string) ([]*endpoint.Endpoint, error) {
	if strings.EqualFold(strings.TrimSpace(labels["traefik.enable"]), "false") {
		return nil, nil
	}

	hosts := extractTraefikHosts(labels)
	if len(hosts) == 0 {
		return nil, nil
	}

	spec, err := parseSpec(defaultTarget, traefikOverrides(prefix, labels))
	if err != nil {
		return nil, err
	}

	var endpoints []*endpoint.Endpoint
	for _, h := range hosts {
		ep, err := buildEndpoint(h, spec)
		if err != nil {
			return nil, fmt.Errorf("traefik host %q: %w (set --default-target or %s.target)", h, err, prefix)
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// traefikOverrides collects the non-indexed externaldns.* keys used to shape
// Traefik-derived records. The hostname key is meaningless here (the names come
// from the router rules) and is dropped.
func traefikOverrides(prefix string, labels map[string]string) map[string]string {
	p := prefix + "."
	kv := map[string]string{}
	for k, v := range labels {
		rest, ok := strings.CutPrefix(k, p)
		if !ok || strings.Contains(rest, ".") {
			continue // only the non-indexed group shapes Traefik records
		}
		kv[rest] = v
	}
	delete(kv, "hostname")
	return kv
}

var (
	// hostRuleRe matches Host(...) and HostSNI(...) matchers, capturing their
	// argument list. HostRegexp is intentionally not matched: a regexp does not
	// yield a concrete record name.
	hostRuleRe = regexp.MustCompile(`(?i)Host(?:SNI)?\(([^)]*)\)`)
	// backtickRe pulls each backtick-quoted hostname out of an argument list.
	backtickRe = regexp.MustCompile("`([^`]+)`")
)

// extractTraefikHosts returns the de-duplicated, sorted set of hostnames named
// in any Traefik router rule label (http or tcp).
func extractTraefikHosts(labels map[string]string) []string {
	seen := map[string]bool{}
	var hosts []string
	for k, v := range labels {
		if !isTraefikRuleKey(k) {
			continue
		}
		for _, rule := range hostRuleRe.FindAllStringSubmatch(v, -1) {
			for _, m := range backtickRe.FindAllStringSubmatch(rule[1], -1) {
				h := strings.TrimSuffix(strings.TrimSpace(m[1]), ".")
				if h == "" || seen[h] {
					continue
				}
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	sort.Strings(hosts)
	return hosts
}

// isTraefikRuleKey reports whether a label is a router rule, e.g.
// traefik.http.routers.<name>.rule or traefik.tcp.routers.<name>.rule.
func isTraefikRuleKey(k string) bool {
	return strings.HasPrefix(k, "traefik.") && strings.HasSuffix(k, ".rule")
}
