package registry

import (
	"context"
	"testing"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

// fakeProvider records the changes applied to it and returns a fixed record set.
type fakeProvider struct {
	records []*endpoint.Endpoint
	applied *plan.Changes
}

func (f *fakeProvider) Records(context.Context) ([]*endpoint.Endpoint, error) {
	return f.records, nil
}

func (f *fakeProvider) ApplyChanges(_ context.Context, c *plan.Changes) error {
	f.applied = c
	return nil
}

func TestRecordsAnnotatesOwnershipAndHidesTXT(t *testing.T) {
	txt := endpoint.New("externaldns-a-app.example.com", "TXT", 0,
		"heritage=externaldns-controller,externaldns-controller/owner=host-a")
	a := endpoint.New("app.example.com", "A", 0, "203.0.113.10")
	other := endpoint.New("manual.example.com", "A", 0, "203.0.113.20")

	reg := New(&fakeProvider{records: []*endpoint.Endpoint{txt, a, other}}, "host-a", "externaldns-")

	got, err := reg.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("expected TXT record hidden, got %d records", len(got))
	}
	byKey := map[string]*endpoint.Endpoint{}
	for _, e := range got {
		byKey[e.Key()] = e
	}
	if owner := byKey["A/app.example.com"].Owner(); owner != "host-a" {
		t.Errorf("app.example.com owner = %q, want host-a", owner)
	}
	if owner := byKey["A/manual.example.com"].Owner(); owner != "" {
		t.Errorf("manual record should be unowned, got %q", owner)
	}
}

func TestApplyChangesAddsCompanionTXT(t *testing.T) {
	fp := &fakeProvider{}
	reg := New(fp, "host-a", "externaldns-")

	create := endpoint.New("app.example.com", "A", 0, "203.0.113.10")
	del := endpoint.New("old.example.com", "A", 0, "203.0.113.99")

	err := reg.ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{create},
		Delete: []*endpoint.Endpoint{del},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(fp.applied.Create) != 2 {
		t.Fatalf("expected record + TXT on create, got %d", len(fp.applied.Create))
	}
	if len(fp.applied.Delete) != 2 {
		t.Fatalf("expected record + TXT on delete, got %d", len(fp.applied.Delete))
	}

	if !hasTXT(fp.applied.Create, "externaldns-a-app.example.com") {
		t.Errorf("missing companion TXT for created record")
	}
	if !hasTXT(fp.applied.Delete, "externaldns-a-old.example.com") {
		t.Errorf("missing companion TXT for deleted record")
	}
}

func TestOwnershipRoundTrip(t *testing.T) {
	reg := New(&fakeProvider{}, "host-a", "externaldns-")
	ep := endpoint.New("app.example.com", "A", 0, "203.0.113.10")
	txt := reg.txtFor(ep)

	key, owner, ok := reg.parseOwnership(txt)
	if !ok {
		t.Fatal("txt record not recognised as ownership record")
	}
	if key != "A/app.example.com" {
		t.Errorf("key = %q", key)
	}
	if owner != "host-a" {
		t.Errorf("owner = %q", owner)
	}
}

func hasTXT(eps []*endpoint.Endpoint, name string) bool {
	for _, e := range eps {
		if e.RecordType == endpoint.RecordTypeTXT && e.DNSName == name {
			return true
		}
	}
	return false
}
