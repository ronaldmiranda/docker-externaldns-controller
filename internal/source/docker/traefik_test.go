package docker

import (
	"reflect"
	"testing"
)

func TestTraefikHostRuleWithDefaultTarget(t *testing.T) {
	eps, err := parseTraefikLabels("externaldns", "203.0.113.10", map[string]string{
		"traefik.enable":                     "true",
		"traefik.http.routers.app.rule":      "Host(`app.example.com`)",
		"traefik.http.routers.app.entryp...": "web", // unrelated key ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(eps))
	}
	ep := eps[0]
	if ep.DNSName != "app.example.com" || ep.RecordType != "A" {
		t.Errorf("got %s/%s", ep.DNSName, ep.RecordType)
	}
	if !reflect.DeepEqual(ep.Targets, []string{"203.0.113.10"}) {
		t.Errorf("targets = %v", ep.Targets)
	}
}

func TestTraefikMultipleHostsAndMatchers(t *testing.T) {
	eps, err := parseTraefikLabels("externaldns", "203.0.113.10", map[string]string{
		"traefik.http.routers.web.rule": "Host(`a.example.com`, `b.example.com`) && PathPrefix(`/`)",
		"traefik.http.routers.api.rule": "Host(`a.example.com`) || Host(`c.example.com`)",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, ep := range eps {
		got[ep.DNSName] = true
	}
	for _, want := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		if !got[want] {
			t.Errorf("missing host %s (got %v)", want, got)
		}
	}
	if len(eps) != 3 {
		t.Errorf("expected 3 de-duplicated hosts, got %d", len(eps))
	}
}

func TestTraefikHostSNI(t *testing.T) {
	eps, err := parseTraefikLabels("externaldns", "203.0.113.10", map[string]string{
		"traefik.tcp.routers.db.rule": "HostSNI(`db.example.com`)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].DNSName != "db.example.com" {
		t.Fatalf("got %v", eps)
	}
}

func TestTraefikOverrideSpec(t *testing.T) {
	// Non-indexed externaldns.* keys shape the Traefik-derived record.
	eps, err := parseTraefikLabels("externaldns", "", map[string]string{
		"traefik.http.routers.app.rule": "Host(`app.example.com`)",
		"externaldns.type":              "CNAME",
		"externaldns.target":            "tunnel.example.net",
		"externaldns.proxied":           "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep := eps[0]
	if ep.RecordType != "CNAME" || ep.Targets[0] != "tunnel.example.net" {
		t.Errorf("got %s -> %v", ep.RecordType, ep.Targets)
	}
	if ep.ProviderSpecific["proxied"] != "true" {
		t.Errorf("proxied = %q", ep.ProviderSpecific["proxied"])
	}
}

func TestTraefikDisabled(t *testing.T) {
	eps, err := parseTraefikLabels("externaldns", "203.0.113.10", map[string]string{
		"traefik.enable":                "false",
		"traefik.http.routers.app.rule": "Host(`app.example.com`)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Errorf("traefik.enable=false must yield no records, got %v", eps)
	}
}

func TestTraefikNoTargetIsError(t *testing.T) {
	// A Host rule with neither a default target nor an override cannot become a
	// record.
	if _, err := parseTraefikLabels("externaldns", "", map[string]string{
		"traefik.http.routers.app.rule": "Host(`app.example.com`)",
	}); err == nil {
		t.Error("expected error when no target is resolvable")
	}
}

func TestTraefikHostRegexpIgnored(t *testing.T) {
	eps, err := parseTraefikLabels("externaldns", "203.0.113.10", map[string]string{
		"traefik.http.routers.app.rule": "HostRegexp(`^app\\..+$`)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Errorf("HostRegexp must not produce records, got %v", eps)
	}
}
