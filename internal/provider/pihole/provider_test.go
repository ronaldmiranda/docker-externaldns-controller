package pihole

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

// fakePihole is a minimal in-memory Pi-hole v6 API for testing the provider.
type fakePihole struct {
	mu       sync.Mutex
	password string
	sid      string
	hosts    map[string]bool // "<ip> <hostname>" -> present
	authHits int
}

func newFakePihole(password string, hosts ...string) *fakePihole {
	f := &fakePihole{password: password, sid: "SID-123", hosts: map[string]bool{}}
	for _, h := range hosts {
		f.hosts[h] = true
	}
	return f
}

func (f *fakePihole) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authHits++
		var body struct {
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"valid": false, "message": "password incorrect"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"valid": true, "sid": f.sid, "validity": 1800, "message": "password correct"},
		})
	})

	mux.HandleFunc("/api/config/dns/hosts", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var list []string
		for h := range f.hosts {
			list = append(list, h)
		}
		sort.Strings(list)
		json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{"dns": map[string]any{"hosts": list}},
		})
	})

	// PUT/DELETE /api/config/dns/hosts/<value>
	mux.HandleFunc("/api/config/dns/hosts/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed(w, r) {
			return
		}
		raw := strings.TrimPrefix(r.URL.EscapedPath(), "/api/config/dns/hosts/")
		value, err := url.PathUnescape(raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			f.hosts[value] = true
		case http.MethodDelete:
			delete(f.hosts, value)
		}
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

func (f *fakePihole) authed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-FTL-SID") != f.sid {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func testProvider(t *testing.T, f *fakePihole, domainFilter ...string) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	p, err := New(srv.URL, f.password, false, domainFilter, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return p, srv
}

func TestRecordsParsesHosts(t *testing.T) {
	f := newFakePihole("secret", "1.1.1.1 teste.teste", "fd00::1 v6.example.com")
	p, srv := testProvider(t, f)
	defer srv.Close()

	recs, err := p.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*endpoint.Endpoint{}
	for _, r := range recs {
		byKey[r.Key()] = r
	}
	if got := byKey["A/teste.teste"]; got == nil || got.Targets[0] != "1.1.1.1" {
		t.Errorf("A record wrong: %+v", got)
	}
	if got := byKey["AAAA/v6.example.com"]; got == nil || got.Targets[0] != "fd00::1" {
		t.Errorf("AAAA record wrong: %+v", got)
	}
}

func TestApplyCreateAndDelete(t *testing.T) {
	f := newFakePihole("secret", "9.9.9.9 old.example.com")
	p, srv := testProvider(t, f)
	defer srv.Close()

	create := endpoint.New("app.example.com", "A", 0, "1.1.1.1")
	del := endpoint.New("old.example.com", "A", 0, "9.9.9.9")

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{create},
		Delete: []*endpoint.Endpoint{del},
	})
	if err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hosts["1.1.1.1 app.example.com"] {
		t.Errorf("create did not add host: %v", f.hosts)
	}
	if f.hosts["9.9.9.9 old.example.com"] {
		t.Errorf("delete did not remove host: %v", f.hosts)
	}
}

func TestApplyUpdateReplacesTargets(t *testing.T) {
	f := newFakePihole("secret", "1.1.1.1 app.example.com")
	p, srv := testProvider(t, f)
	defer srv.Close()

	old := endpoint.New("app.example.com", "A", 0, "1.1.1.1")
	nw := endpoint.New("app.example.com", "A", 0, "2.2.2.2")

	err := p.ApplyChanges(context.Background(), &plan.Changes{
		UpdateOld: []*endpoint.Endpoint{old},
		UpdateNew: []*endpoint.Endpoint{nw},
	})
	if err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hosts["1.1.1.1 app.example.com"] {
		t.Errorf("stale target not removed: %v", f.hosts)
	}
	if !f.hosts["2.2.2.2 app.example.com"] {
		t.Errorf("new target not added: %v", f.hosts)
	}
}

func TestDomainFilterScopesRecords(t *testing.T) {
	f := newFakePihole("secret", "1.1.1.1 app.example.com", "2.2.2.2 router.lan")
	p, srv := testProvider(t, f, "example.com")
	defer srv.Close()

	recs, err := p.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].DNSName != "app.example.com" {
		t.Errorf("domain filter not applied: %+v", recs)
	}
}

func TestUnsupportedTypeSkipped(t *testing.T) {
	f := newFakePihole("secret")
	p, srv := testProvider(t, f)
	defer srv.Close()

	cname := endpoint.New("www.example.com", "CNAME", 0, "app.example.com")
	if err := p.ApplyChanges(context.Background(), &plan.Changes{Create: []*endpoint.Endpoint{cname}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hosts) != 0 {
		t.Errorf("CNAME should not have been written to hosts: %v", f.hosts)
	}
}

func TestAuthenticatesOnceAndReuses(t *testing.T) {
	f := newFakePihole("secret", "1.1.1.1 a.example.com")
	p, srv := testProvider(t, f)
	defer srv.Close()

	for range 3 {
		if _, err := p.Records(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authHits != 1 {
		t.Errorf("expected a single authentication, got %d", f.authHits)
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFakePihole("secret")
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	p, err := New(srv.URL, "wrong", false, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Records(context.Background()); err == nil {
		t.Error("expected auth failure with wrong password")
	}
}
