// Package provider defines the contract every DNS backend must satisfy. Adding
// support for a new cloud (Route53, DigitalOcean, ...) means implementing this
// interface and registering it in the factory used by cmd — nothing else in
// the controller changes.
package provider

import (
	"context"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

// Provider is the minimal surface the controller needs from a DNS backend.
//
// Implementations must be safe to call sequentially from a single reconcile
// loop; they do not need to be goroutine-safe.
type Provider interface {
	// Records returns every DNS record the provider currently holds within the
	// zones it manages, expressed as endpoints. Records of types the controller
	// does not manage may be omitted.
	Records(ctx context.Context) ([]*endpoint.Endpoint, error)

	// ApplyChanges creates, updates and deletes records to realise the plan.
	// A change set that only partially applies should return an error; the
	// controller logs it and retries on the next reconcile.
	ApplyChanges(ctx context.Context, changes *plan.Changes) error
}
