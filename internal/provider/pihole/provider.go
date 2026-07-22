package pihole

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

// Provider manages Pi-hole local DNS hosts entries. Each entry is an
// "<ip> <hostname>" pair, i.e. an A record (IPv4) or AAAA record (IPv6). Record
// types Pi-hole's hosts list cannot express (CNAME, TXT) are skipped with a
// warning.
type Provider struct {
	client       *client
	domainFilter []string
	log          *slog.Logger
}

// New returns a Pi-hole provider. rawURL points at the instance root
// (e.g. http://pi.hole); password is the web/API password. domainFilter, when
// set, restricts management to hostnames within those domains — strongly
// recommended, since this provider is paired with the noop registry and would
// otherwise treat every local DNS entry as its own.
func New(rawURL, password string, tlsInsecure bool, domainFilter []string, log *slog.Logger) (*Provider, error) {
	c, err := newClient(rawURL, password, tlsInsecure)
	if err != nil {
		return nil, err
	}
	return &Provider{client: c, domainFilter: domainFilter, log: log}, nil
}

// Records reads the local DNS hosts list and turns each "<ip> <hostname>" entry
// into an A/AAAA endpoint, grouping shared names into a single endpoint.
func (p *Provider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	hosts, err := p.client.getHosts(ctx)
	if err != nil {
		return nil, err
	}

	byKey := map[string]*endpoint.Endpoint{}
	var order []string
	for _, entry := range hosts {
		ip, names, ok := splitHostEntry(entry)
		if !ok {
			p.log.Warn("ignoring malformed pihole hosts entry", "entry", entry)
			continue
		}
		recordType := recordTypeForIP(ip)
		for _, name := range names {
			if !p.inScope(name) {
				continue
			}
			ep := endpoint.New(name, recordType, endpoint.TTLDefault)
			key := ep.Key()
			existing, seen := byKey[key]
			if !seen {
				existing = ep
				byKey[key] = existing
				order = append(order, key)
			}
			existing.Targets = append(existing.Targets, ip)
		}
	}

	endpoints := make([]*endpoint.Endpoint, 0, len(order))
	for _, key := range order {
		endpoints = append(endpoints, byKey[key])
	}
	return endpoints, nil
}

// ApplyChanges realises the plan as add/remove operations on the hosts list.
func (p *Provider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	var errs []string
	record := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	for _, ep := range changes.Create {
		record(p.addTargets(ctx, ep, ep.Targets))
	}
	// UpdateOld and UpdateNew are index-aligned pairs from the plan.
	for i := range changes.UpdateNew {
		nw := changes.UpdateNew[i]
		old := changes.UpdateOld[i]
		record(p.deleteTargets(ctx, old, missing(old.Targets, nw.Targets)))
		record(p.addTargets(ctx, nw, missing(nw.Targets, old.Targets)))
	}
	for _, ep := range changes.Delete {
		record(p.deleteTargets(ctx, ep, ep.Targets))
	}

	if len(errs) > 0 {
		return fmt.Errorf("pihole apply: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (p *Provider) addTargets(ctx context.Context, ep *endpoint.Endpoint, targets []string) error {
	if !p.supported(ep) {
		return nil
	}
	for _, t := range targets {
		p.log.Info("adding pihole host", "name", ep.DNSName, "target", t)
		if err := p.client.addHost(ctx, hostEntry(t, ep.DNSName)); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) deleteTargets(ctx context.Context, ep *endpoint.Endpoint, targets []string) error {
	if !p.supported(ep) {
		return nil
	}
	for _, t := range targets {
		p.log.Info("removing pihole host", "name", ep.DNSName, "target", t)
		if err := p.client.deleteHost(ctx, hostEntry(t, ep.DNSName)); err != nil {
			return err
		}
	}
	return nil
}

// supported reports whether an endpoint maps onto Pi-hole's hosts list. Others
// are skipped (Pi-hole has no place to store them).
func (p *Provider) supported(ep *endpoint.Endpoint) bool {
	if ep.RecordType == endpoint.RecordTypeA || ep.RecordType == endpoint.RecordTypeAAAA {
		return true
	}
	p.log.Warn("pihole provider skips record type it cannot store",
		"name", ep.DNSName, "type", ep.RecordType)
	return false
}

func (p *Provider) inScope(name string) bool {
	if len(p.domainFilter) == 0 {
		return true
	}
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	for _, d := range p.domainFilter {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		if n == d || strings.HasSuffix(n, "."+d) {
			return true
		}
	}
	return false
}

// hostEntry builds the "<ip> <hostname>" value Pi-hole stores.
func hostEntry(ip, hostname string) string {
	return ip + " " + hostname
}

// splitHostEntry parses "<ip> <hostname> [more-hostnames...]" into its IP and
// the hostnames it maps to.
func splitHostEntry(entry string) (ip string, names []string, ok bool) {
	fields := strings.Fields(entry)
	if len(fields) < 2 {
		return "", nil, false
	}
	return fields[0], fields[1:], true
}

func recordTypeForIP(ip string) string {
	if strings.Contains(ip, ":") {
		return endpoint.RecordTypeAAAA
	}
	return endpoint.RecordTypeA
}

// missing returns the elements of a that are not present in b.
func missing(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	var out []string
	for _, v := range a {
		if !set[v] {
			out = append(out, v)
		}
	}
	return out
}
