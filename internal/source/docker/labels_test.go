package docker

import (
	"sort"
	"testing"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

func sortEndpoints(eps []*endpoint.Endpoint) {
	sort.Slice(eps, func(i, j int) bool { return eps[i].Key() < eps[j].Key() })
}

func TestParseLabelsSingleARecord(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.hostname": "app.example.com",
		"externaldns.target":   "203.0.113.10",
		"com.docker.compose":   "ignored",
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
	if len(ep.Targets) != 1 || ep.Targets[0] != "203.0.113.10" {
		t.Errorf("targets = %v", ep.Targets)
	}
}

func TestParseLabelsDefaultTarget(t *testing.T) {
	eps, err := parseLabels("externaldns", "198.51.100.7", map[string]string{
		"externaldns.hostname": "app.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if eps[0].Targets[0] != "198.51.100.7" {
		t.Errorf("expected default target, got %v", eps[0].Targets)
	}
}

func TestParseLabelsCNAMEProxiedTTL(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.hostname": "www.example.com",
		"externaldns.type":     "CNAME",
		"externaldns.target":   "app.example.com",
		"externaldns.ttl":      "300",
		"externaldns.proxied":  "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep := eps[0]
	if ep.RecordType != "CNAME" {
		t.Errorf("type = %s", ep.RecordType)
	}
	if ep.RecordTTL != endpoint.TTL(300) {
		t.Errorf("ttl = %d", ep.RecordTTL)
	}
	if ep.ProviderSpecific["proxied"] != "true" {
		t.Errorf("proxied = %q", ep.ProviderSpecific["proxied"])
	}
}

func TestParseLabelsMultipleTargets(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.hostname": "app.example.com",
		"externaldns.target":   "203.0.113.10, 203.0.113.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps[0].Targets) != 2 {
		t.Fatalf("targets = %v", eps[0].Targets)
	}
}

func TestParseLabelsMultipleRecords(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.0.hostname": "app.example.com",
		"externaldns.0.target":   "203.0.113.10",
		"externaldns.1.hostname": "www.example.com",
		"externaldns.1.type":     "CNAME",
		"externaldns.1.target":   "app.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	sortEndpoints(eps)
	want := []struct{ name, typ string }{
		{"app.example.com", "A"},
		{"www.example.com", "CNAME"},
	}
	if len(eps) != len(want) {
		t.Fatalf("got %d endpoints, want %d", len(eps), len(want))
	}
	for i, w := range want {
		if eps[i].DNSName != w.name || eps[i].RecordType != w.typ {
			t.Errorf("endpoint %d = %s/%s, want %s/%s", i, eps[i].DNSName, eps[i].RecordType, w.name, w.typ)
		}
	}
}

func TestParseLabelsErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"missing target": {"externaldns.hostname": "app.example.com", "externaldns.type": "CNAME"},
		"bad type":       {"externaldns.hostname": "app.example.com", "externaldns.type": "SRV", "externaldns.target": "x"},
		"bad ttl":        {"externaldns.hostname": "app.example.com", "externaldns.target": "1.2.3.4", "externaldns.ttl": "soon"},
		"bad proxied":    {"externaldns.hostname": "app.example.com", "externaldns.target": "1.2.3.4", "externaldns.proxied": "yes"},
	}
	for name, labels := range cases {
		if _, err := parseLabels("externaldns", "", labels); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseLabelsNoHostnameIsIgnored(t *testing.T) {
	// Keys without a hostname are not a native record (they may shape Traefik
	// records instead), so the native parser yields nothing without erroring.
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.target": "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Errorf("expected no records, got %v", eps)
	}
}

func TestParseLabelsInferAAAA(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{
		"externaldns.hostname": "app.example.com",
		"externaldns.target":   "2001:db8::1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if eps[0].RecordType != "AAAA" {
		t.Errorf("expected AAAA inferred from IPv6 target, got %s", eps[0].RecordType)
	}
}

func TestParseLabelsNoMatch(t *testing.T) {
	eps, err := parseLabels("externaldns", "", map[string]string{"traefik.enable": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Errorf("expected no endpoints, got %v", eps)
	}
}
