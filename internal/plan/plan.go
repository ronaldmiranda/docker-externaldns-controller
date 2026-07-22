// Package plan diffs the desired set of endpoints against what currently exists
// in a provider and produces the minimal set of changes to apply. It is the
// heart of the controller and is deliberately free of any provider or source
// dependency so it can be unit-tested in isolation.
package plan

import (
	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

// Changes is the output of a plan: records to create, update (old + new pairs)
// and delete.
type Changes struct {
	Create    []*endpoint.Endpoint
	UpdateOld []*endpoint.Endpoint
	UpdateNew []*endpoint.Endpoint
	Delete    []*endpoint.Endpoint
}

// Empty reports whether the plan is a no-op.
func (c *Changes) Empty() bool {
	return len(c.Create) == 0 && len(c.UpdateNew) == 0 && len(c.Delete) == 0
}

// Plan holds the inputs and, after Calculate, the resulting Changes.
type Plan struct {
	// Current is everything the provider reports, annotated by the registry
	// with ownership labels.
	Current []*endpoint.Endpoint
	// Desired is what the sources want to exist.
	Desired []*endpoint.Endpoint
	// OwnerID is this controller instance's identity. Only records carrying
	// this owner are ever updated or deleted.
	OwnerID string

	// Changes is populated by Calculate.
	Changes *Changes
	// Conflicts lists desired endpoints that could not be created because an
	// unmanaged record already occupies the name. They are reported, never
	// touched.
	Conflicts []*endpoint.Endpoint
}

// Calculate compares Desired against Current and fills in Changes and
// Conflicts. It returns the receiver for convenience.
//
// Rules:
//   - A desired record with no current record of the same (name, type) is
//     created and claimed for this owner.
//   - A desired record whose current record is owned by us is updated when its
//     targets, TTL or provider-specific settings differ.
//   - A desired record whose current record is owned by someone else (or is
//     unmanaged) is a conflict: left untouched and reported.
//   - A current record owned by us with no matching desired record is deleted.
func (p *Plan) Calculate() *Plan {
	changes := &Changes{}

	currentByKey := make(map[string]*endpoint.Endpoint, len(p.Current))
	for _, e := range p.Current {
		currentByKey[e.Key()] = e
	}
	desiredByKey := make(map[string]*endpoint.Endpoint, len(p.Desired))

	for _, d := range p.Desired {
		if _, dup := desiredByKey[d.Key()]; dup {
			// A source produced two declarations for the same record; keep the
			// first and ignore the rest so the plan stays deterministic.
			continue
		}
		desiredByKey[d.Key()] = d
		d.WithLabel(endpoint.OwnerLabelKey, p.OwnerID)

		cur, exists := currentByKey[d.Key()]
		if !exists {
			changes.Create = append(changes.Create, d)
			continue
		}
		if cur.Owner() != p.OwnerID {
			p.Conflicts = append(p.Conflicts, d)
			continue
		}
		if needsUpdate(cur, d) {
			changes.UpdateOld = append(changes.UpdateOld, cur)
			changes.UpdateNew = append(changes.UpdateNew, d)
		}
	}

	for _, c := range p.Current {
		if c.Owner() != p.OwnerID {
			continue
		}
		if _, wanted := desiredByKey[c.Key()]; !wanted {
			changes.Delete = append(changes.Delete, c)
		}
	}

	p.Changes = changes
	return p
}

// needsUpdate reports whether the live record differs from the desired one in
// any field the controller manages.
func needsUpdate(current, desired *endpoint.Endpoint) bool {
	if !current.SameTargets(desired) {
		return true
	}
	if desired.RecordTTL.IsConfigured() && current.RecordTTL != desired.RecordTTL {
		return true
	}
	for k, want := range desired.ProviderSpecific {
		if current.ProviderSpecific[k] != want {
			return true
		}
	}
	return false
}
