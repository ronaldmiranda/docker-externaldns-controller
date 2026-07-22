package registry

import (
	"context"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
	"github.com/ronaldmiranda/externaldns-controller/internal/provider"
)

// NoopRegistry tracks no ownership: it claims every record the provider returns
// as its own, so the controller manages the full set of records within the
// provider's scope. Use it for providers that cannot store the companion TXT
// records the TXTRegistry relies on (e.g. Pi-hole's local DNS, which only holds
// A/AAAA and CNAME entries).
//
// Because there is no ownership marker, everything in scope is fair game for
// deletion — narrow the scope with a domain filter so the controller does not
// remove hand-made entries.
type NoopRegistry struct {
	provider provider.Provider
	ownerID  string
}

// NewNoop returns a registry that owns whatever the provider reports.
func NewNoop(p provider.Provider, ownerID string) *NoopRegistry {
	return &NoopRegistry{provider: p, ownerID: ownerID}
}

// Records returns the provider's records, each stamped with this controller's
// owner id so the plan treats them as managed.
func (r *NoopRegistry) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	records, err := r.provider.Records(ctx)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		rec.WithLabel(endpoint.OwnerLabelKey, r.ownerID)
	}
	return records, nil
}

// ApplyChanges forwards the plan unchanged; there are no companion records to
// maintain.
func (r *NoopRegistry) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	return r.provider.ApplyChanges(ctx, changes)
}
