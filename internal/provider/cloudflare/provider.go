package cloudflare

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
)

// managedTypes are the record types this provider reads back into endpoints.
var managedTypes = []string{
	endpoint.RecordTypeA,
	endpoint.RecordTypeAAAA,
	endpoint.RecordTypeCNAME,
	endpoint.RecordTypeTXT,
}

// ttlAutomatic is Cloudflare's sentinel meaning "let Cloudflare choose".
const ttlAutomatic = 1

// Provider manages DNS records in Cloudflare zones. It implements
// provider.Provider.
type Provider struct {
	client *client
	// domainFilter, when non-empty, restricts management to zones whose name
	// equals or is a parent of one of the listed domains.
	domainFilter []string
	log          *slog.Logger
}

// New returns a Cloudflare provider authenticated with the given API token.
// domainFilter may be nil to manage every zone the token can see.
func New(token string, domainFilter []string, log *slog.Logger) *Provider {
	return &Provider{client: newClient(token), domainFilter: domainFilter, log: log}
}

// Records lists every managed record across the visible (and filtered) zones,
// grouping records that share a name and type into a single endpoint.
func (p *Provider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	zones, err := p.managedZones(ctx)
	if err != nil {
		return nil, err
	}

	// key: name/type -> endpoint being assembled.
	byKey := map[string]*endpoint.Endpoint{}
	var order []string
	for _, z := range zones {
		records, err := p.client.listDNSRecords(ctx, z.ID, nil)
		if err != nil {
			return nil, fmt.Errorf("listing records in zone %s: %w", z.Name, err)
		}
		for _, rec := range records {
			if !isManagedType(rec.Type) {
				continue
			}
			ep := endpoint.New(rec.Name, rec.Type, ttlFromCloudflare(rec.TTL))
			key := ep.Key()
			existing, ok := byKey[key]
			if !ok {
				existing = ep
				byKey[key] = existing
				order = append(order, key)
			}
			existing.Targets = append(existing.Targets, normalizeContent(rec.Type, rec.Content))
			if rec.Type != endpoint.RecordTypeTXT {
				existing.ProviderSpecific["proxied"] = strconv.FormatBool(rec.Proxied)
			}
		}
	}

	endpoints := make([]*endpoint.Endpoint, 0, len(order))
	for _, key := range order {
		endpoints = append(endpoints, byKey[key])
	}
	return endpoints, nil
}

// ApplyChanges realises the plan. Creates and updates are reconciled to the
// exact desired target set per (name, type); deletes remove every record of
// that name and type. Each change is scoped to the zone that owns its hostname.
func (p *Provider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	var errs []string
	record := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	for _, ep := range changes.Create {
		record(p.reconcileRecord(ctx, ep))
	}
	for _, ep := range changes.UpdateNew {
		record(p.reconcileRecord(ctx, ep))
	}
	for _, ep := range changes.Delete {
		record(p.deleteRecord(ctx, ep))
	}

	if len(errs) > 0 {
		return fmt.Errorf("cloudflare apply: %s", strings.Join(errs, "; "))
	}
	return nil
}

// reconcileRecord makes the live records for ep's (name, type) match ep exactly:
// missing targets are created, stale ones deleted, and proxied/TTL differences
// are corrected in place.
func (p *Provider) reconcileRecord(ctx context.Context, ep *endpoint.Endpoint) error {
	zone, err := p.client.zoneForHostname(ctx, ep.DNSName)
	if err != nil {
		return err
	}

	live, err := p.recordsFor(ctx, zone.ID, ep)
	if err != nil {
		return err
	}

	proxied := ep.ProviderSpecific["proxied"] == "true"
	ttl := ttlToCloudflare(ep.RecordTTL, proxied)

	desired := map[string]bool{}
	for _, target := range ep.Targets {
		desired[normalizeContent(ep.RecordType, target)] = true
	}

	// Update or keep records whose content is still desired; delete the rest.
	kept := map[string]bool{}
	for _, rec := range live {
		content := normalizeContent(rec.Type, rec.Content)
		if !desired[content] {
			p.log.Info("deleting DNS record", "name", ep.DNSName, "type", ep.RecordType, "content", rec.Content)
			if err := p.client.deleteDNSRecord(ctx, zone.ID, rec.ID); err != nil {
				return err
			}
			continue
		}
		kept[content] = true
		if rec.Proxied != proxied || rec.TTL != ttl {
			rec.Proxied = proxied
			rec.TTL = ttl
			p.log.Info("updating DNS record", "name", ep.DNSName, "type", ep.RecordType, "content", rec.Content)
			if err := p.client.updateDNSRecord(ctx, zone.ID, rec); err != nil {
				return err
			}
		}
	}

	// Create any desired content that has no live record yet.
	for _, target := range ep.Targets {
		content := normalizeContent(ep.RecordType, target)
		if kept[content] {
			continue
		}
		p.log.Info("creating DNS record", "name", ep.DNSName, "type", ep.RecordType, "content", content)
		err := p.client.createDNSRecord(ctx, zone.ID, dnsRecord{
			Type:    ep.RecordType,
			Name:    ep.DNSName,
			Content: content,
			Proxied: proxied,
			TTL:     ttl,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// deleteRecord removes every live record of ep's name and type.
func (p *Provider) deleteRecord(ctx context.Context, ep *endpoint.Endpoint) error {
	zone, err := p.client.zoneForHostname(ctx, ep.DNSName)
	if err != nil {
		return err
	}
	live, err := p.recordsFor(ctx, zone.ID, ep)
	if err != nil {
		return err
	}
	for _, rec := range live {
		p.log.Info("deleting DNS record", "name", ep.DNSName, "type", ep.RecordType, "content", rec.Content)
		if err := p.client.deleteDNSRecord(ctx, zone.ID, rec.ID); err != nil {
			return err
		}
	}
	return nil
}

// recordsFor lists the live records matching ep's name and type.
func (p *Provider) recordsFor(ctx context.Context, zoneID string, ep *endpoint.Endpoint) ([]dnsRecord, error) {
	return p.client.listDNSRecords(ctx, zoneID, map[string]string{
		"type": ep.RecordType,
		"name": ep.DNSName,
	})
}

// managedZones returns the zones this provider is allowed to touch.
func (p *Provider) managedZones(ctx context.Context) ([]Zone, error) {
	zones, err := p.client.Zones(ctx)
	if err != nil {
		return nil, err
	}
	if len(p.domainFilter) == 0 {
		return zones, nil
	}
	var kept []Zone
	for _, z := range zones {
		if p.zoneMatchesFilter(z.Name) {
			kept = append(kept, z)
		}
	}
	return kept, nil
}

func (p *Provider) zoneMatchesFilter(zoneName string) bool {
	zn := strings.ToLower(strings.TrimSuffix(zoneName, "."))
	for _, d := range p.domainFilter {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		if d == zn || strings.HasSuffix(d, "."+zn) || strings.HasSuffix(zn, "."+d) {
			return true
		}
	}
	return false
}

func isManagedType(t string) bool {
	for _, m := range managedTypes {
		if strings.EqualFold(t, m) {
			return true
		}
	}
	return false
}

// normalizeContent makes record contents comparable: hostnames are
// lower-cased and de-trailing-dotted; TXT values are stripped of the surrounding
// quotes Cloudflare stores them with.
func normalizeContent(recordType, content string) string {
	switch strings.ToUpper(recordType) {
	case endpoint.RecordTypeCNAME:
		return strings.ToLower(strings.TrimSuffix(content, "."))
	case endpoint.RecordTypeTXT:
		return strings.Trim(content, "\"")
	default:
		return content
	}
}

func ttlFromCloudflare(ttl int) endpoint.TTL {
	if ttl <= ttlAutomatic {
		return endpoint.TTLDefault
	}
	return endpoint.TTL(ttl)
}

func ttlToCloudflare(ttl endpoint.TTL, proxied bool) int {
	// Proxied records must use automatic TTL; Cloudflare rejects anything else.
	if proxied || !ttl.IsConfigured() {
		return ttlAutomatic
	}
	return int(ttl)
}
