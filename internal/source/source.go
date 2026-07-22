// Package source defines where desired endpoints come from. Today the only
// implementation is Docker container labels, but the interface leaves room for
// Podman or other runtimes without touching the controller.
package source

import (
	"context"
	"time"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
)

// Source produces the DNS endpoints the controller should ensure exist.
type Source interface {
	// Endpoints returns the currently desired set of endpoints.
	Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error)

	// Watch returns a channel that receives a signal (coalesced within
	// debounce) whenever the desired set may have changed. The channel is
	// closed when ctx is cancelled.
	Watch(ctx context.Context, debounce time.Duration) <-chan struct{}

	// Close releases any resources held by the source.
	Close() error
}
