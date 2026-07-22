package plan

import (
	"testing"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

const owner = "host-a"

func owned(ep *endpoint.Endpoint, ownerID string) *endpoint.Endpoint {
	return ep.WithLabel(endpoint.OwnerLabelKey, ownerID)
}

func keys(eps []*endpoint.Endpoint) map[string]bool {
	m := map[string]bool{}
	for _, e := range eps {
		m[e.Key()] = true
	}
	return m
}

func TestCreateWhenMissing(t *testing.T) {
	p := (&Plan{
		Desired: []*endpoint.Endpoint{endpoint.New("app.example.com", "A", 0, "203.0.113.10")},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.Create) != 1 {
		t.Fatalf("create = %d, want 1", len(p.Changes.Create))
	}
	if p.Changes.Create[0].Owner() != owner {
		t.Errorf("created endpoint not claimed: owner=%q", p.Changes.Create[0].Owner())
	}
}

func TestNoChangeWhenEqual(t *testing.T) {
	rec := endpoint.New("app.example.com", "A", 0, "203.0.113.10")
	p := (&Plan{
		Current: []*endpoint.Endpoint{owned(endpoint.New("app.example.com", "A", 0, "203.0.113.10"), owner)},
		Desired: []*endpoint.Endpoint{rec},
		OwnerID: owner,
	}).Calculate()

	if !p.Changes.Empty() {
		t.Errorf("expected no changes, got %+v", p.Changes)
	}
}

func TestUpdateWhenTargetsDiffer(t *testing.T) {
	p := (&Plan{
		Current: []*endpoint.Endpoint{owned(endpoint.New("app.example.com", "A", 0, "203.0.113.10"), owner)},
		Desired: []*endpoint.Endpoint{endpoint.New("app.example.com", "A", 0, "203.0.113.99")},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.UpdateNew) != 1 {
		t.Fatalf("update = %d, want 1", len(p.Changes.UpdateNew))
	}
	if len(p.Changes.Create) != 0 || len(p.Changes.Delete) != 0 {
		t.Errorf("unexpected create/delete: %+v", p.Changes)
	}
}

func TestDeleteWhenOwnedAndUndesired(t *testing.T) {
	p := (&Plan{
		Current: []*endpoint.Endpoint{owned(endpoint.New("old.example.com", "A", 0, "203.0.113.10"), owner)},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.Delete) != 1 {
		t.Fatalf("delete = %d, want 1", len(p.Changes.Delete))
	}
}

func TestUnownedRecordIsNeverDeleted(t *testing.T) {
	p := (&Plan{
		// Present in the zone, owned by nobody: manual record.
		Current: []*endpoint.Endpoint{endpoint.New("manual.example.com", "A", 0, "203.0.113.10")},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.Delete) != 0 {
		t.Errorf("unmanaged record must not be deleted, got %+v", p.Changes.Delete)
	}
}

func TestConflictWhenUnownedRecordExists(t *testing.T) {
	p := (&Plan{
		Current: []*endpoint.Endpoint{endpoint.New("app.example.com", "A", 0, "203.0.113.10")},
		Desired: []*endpoint.Endpoint{endpoint.New("app.example.com", "A", 0, "203.0.113.99")},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.Create) != 0 || len(p.Changes.UpdateNew) != 0 {
		t.Errorf("must not touch unowned record, got %+v", p.Changes)
	}
	if len(p.Conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(p.Conflicts))
	}
}

func TestOtherOwnerRecordIsConflict(t *testing.T) {
	p := (&Plan{
		Current: []*endpoint.Endpoint{owned(endpoint.New("app.example.com", "A", 0, "203.0.113.10"), "host-b")},
		Desired: []*endpoint.Endpoint{endpoint.New("app.example.com", "A", 0, "203.0.113.99")},
		OwnerID: owner,
	}).Calculate()

	if !p.Changes.Empty() {
		t.Errorf("records owned by another instance must be left alone, got %+v", p.Changes)
	}
	if len(p.Conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(p.Conflicts))
	}
}

func TestDuplicateDesiredKeptOnce(t *testing.T) {
	p := (&Plan{
		Desired: []*endpoint.Endpoint{
			endpoint.New("app.example.com", "A", 0, "203.0.113.10"),
			endpoint.New("app.example.com", "A", 0, "203.0.113.11"),
		},
		OwnerID: owner,
	}).Calculate()

	if len(p.Changes.Create) != 1 {
		t.Errorf("duplicate desired key must collapse to one create, got %d", len(p.Changes.Create))
	}
	_ = keys
}
