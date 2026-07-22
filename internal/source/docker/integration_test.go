//go:build integration

package docker

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// Requires a running docker daemon and a container started with:
//
//	docker run -d --rm --name edc-inttest \
//	  --label externaldns.hostname=inttest.example.com \
//	  --label externaldns.target=203.0.113.10 alpine sleep 300
//
// Run with: go test -tags integration ./internal/source/docker
func TestEndpointsIntegration(t *testing.T) {
	s, err := New("externaldns", "", true, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoints, err := s.Endpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range endpoints {
		if ep.DNSName == "inttest.example.com" && ep.RecordType == "A" &&
			len(ep.Targets) == 1 && ep.Targets[0] == "203.0.113.10" {
			return
		}
	}
	t.Fatalf("expected endpoint for inttest.example.com, got %+v", endpoints)
}
