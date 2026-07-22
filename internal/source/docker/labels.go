package docker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

// recordSpec is the record-shaping half of a declaration (everything except the
// DNS name). It is shared by the native label parser and the Traefik parser, so
// both honour type/target/ttl/proxied the same way.
type recordSpec struct {
	recordType string
	targets    []string
	ttl        endpoint.TTL
	proxied    string // "", "true" or "false"
}

// parseLabels turns one container's native labels into the DNS endpoints it
// declares.
//
// Single record (the common case):
//
//	externaldns.hostname=app.example.com
//	externaldns.target=203.0.113.10
//
// Optional keys: type (default A), target (content; comma-separates multiple
// targets), ttl (seconds), proxied (Cloudflare-only, "true"/"false").
//
// Multiple records on one container use an index segment:
//
//	externaldns.0.hostname=app.example.com
//	externaldns.0.target=203.0.113.10
//	externaldns.1.hostname=www.example.com
//	externaldns.1.type=CNAME
//	externaldns.1.target=app.example.com
//
// When target is omitted for an address record the controller falls back to its
// configured default target (typically the Docker host's public IP), so most
// services need only a single hostname label. A group carrying no hostname is
// not a record declaration and is ignored here — its keys may instead shape
// Traefik-derived records (see traefik.go).
func parseLabels(prefix, defaultTarget string, labels map[string]string) ([]*endpoint.Endpoint, error) {
	p := prefix + "."
	groups := map[string]map[string]string{}
	for k, v := range labels {
		if !strings.HasPrefix(k, p) {
			continue
		}
		rest := k[len(p):]
		group, key := "", rest
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			group, key = rest[:i], rest[i+1:]
		}
		if groups[group] == nil {
			groups[group] = map[string]string{}
		}
		groups[group][key] = v
	}

	var endpoints []*endpoint.Endpoint
	for group, kv := range groups {
		hostname := strings.TrimSpace(kv["hostname"])
		if hostname == "" {
			continue // not a native record declaration
		}
		spec, err := parseSpec(defaultTarget, kv)
		if err != nil {
			if group != "" {
				return nil, fmt.Errorf("record %q: %w", group, err)
			}
			return nil, err
		}
		ep, err := buildEndpoint(hostname, spec)
		if err != nil {
			if group != "" {
				return nil, fmt.Errorf("record %q: %w", group, err)
			}
			return nil, err
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// parseSpec reads the record-shaping keys (type, target/value, ttl, proxied)
// from a label group, applying defaultTarget to address records that omit a
// target.
func parseSpec(defaultTarget string, kv map[string]string) (recordSpec, error) {
	spec := recordSpec{recordType: strings.ToUpper(strings.TrimSpace(kv["type"]))}

	spec.targets = splitTargets(kv["target"])
	if len(spec.targets) == 0 && kv["value"] != "" {
		spec.targets = splitTargets(kv["value"]) // alias for TXT readability
	}

	if spec.recordType == "" {
		spec.recordType = inferType(spec.targets)
	}
	switch spec.recordType {
	case endpoint.RecordTypeA, endpoint.RecordTypeAAAA, endpoint.RecordTypeCNAME, endpoint.RecordTypeTXT:
	default:
		return spec, fmt.Errorf("unsupported record type %q", spec.recordType)
	}

	if len(spec.targets) == 0 && isAddressType(spec.recordType) && defaultTarget != "" {
		spec.targets = []string{defaultTarget}
	}

	spec.ttl = endpoint.TTLDefault
	if v := strings.TrimSpace(kv["ttl"]); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return spec, fmt.Errorf("invalid ttl %q", v)
		}
		spec.ttl = endpoint.TTL(n)
	}

	if v := strings.TrimSpace(kv["proxied"]); v != "" {
		if v != "true" && v != "false" {
			return spec, fmt.Errorf("proxied must be true or false, got %q", v)
		}
		spec.proxied = v
	}
	return spec, nil
}

// buildEndpoint assembles a single endpoint from a name and a spec, giving each
// endpoint its own copy of the target slice.
func buildEndpoint(hostname string, spec recordSpec) (*endpoint.Endpoint, error) {
	if len(spec.targets) == 0 {
		return nil, fmt.Errorf("hostname %q: a target is required for record type %s", hostname, spec.recordType)
	}
	targets := append([]string(nil), spec.targets...)
	ep := endpoint.New(hostname, spec.recordType, spec.ttl, targets...)
	if spec.proxied != "" {
		ep.ProviderSpecific["proxied"] = spec.proxied
	}
	return ep, nil
}

// inferType guesses the record type from the targets when none was given: an
// IPv6-looking target yields AAAA, anything else A. CNAME and TXT must be
// declared explicitly.
func inferType(targets []string) string {
	for _, t := range targets {
		if strings.Contains(t, ":") {
			return endpoint.RecordTypeAAAA
		}
	}
	return endpoint.RecordTypeA
}

func isAddressType(t string) bool {
	return t == endpoint.RecordTypeA || t == endpoint.RecordTypeAAAA
}

// isExcluded reports whether a container opts out of DNS management entirely via
// the <prefix>.exclude=true label. This lets a service keep its Traefik (or
// native) labels while telling the controller to ignore it — no DNS record is
// created for the container and any it already owns are removed.
func isExcluded(prefix string, labels map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(labels[prefix+".exclude"]), "true")
}

// splitTargets splits a comma-separated target list, trimming blanks.
func splitTargets(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}
