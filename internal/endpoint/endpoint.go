// Package endpoint defines the provider-agnostic representation of a desired
// or existing DNS record. Sources produce endpoints, providers consume them,
// and the plan package diffs the two. Nothing in this package knows about
// Docker or Cloudflare — that is the whole point of the abstraction.
package endpoint

import (
	"sort"
	"strings"
)

// Supported DNS record types. The controller intentionally handles only the
// record types that map cleanly onto container-declared endpoints; TXT is used
// internally by the ownership registry.
const (
	RecordTypeA     = "A"
	RecordTypeAAAA  = "AAAA"
	RecordTypeCNAME = "CNAME"
	RecordTypeTXT   = "TXT"
)

// OwnerLabelKey is the label the registry stamps onto an endpoint to record
// which controller instance owns it. Records without this label are treated as
// unmanaged and are never updated or deleted.
const OwnerLabelKey = "owner"

// TTL is a record time-to-live in seconds. Zero means "not configured": the
// provider is free to apply its own default (for Cloudflare that is automatic
// TTL).
type TTL int64

// TTLDefault leaves the TTL up to the provider.
const TTLDefault TTL = 0

// IsConfigured reports whether an explicit TTL was requested.
func (t TTL) IsConfigured() bool { return t > 0 }

// ProviderSpecific carries hints that only make sense for a particular
// provider (e.g. Cloudflare's "proxied" flag). Providers ignore keys they do
// not understand, so a compose file can stay portable.
type ProviderSpecific map[string]string

// Endpoint is a single DNS name of one record type, with one or more targets
// (e.g. an A record with several IPs). It is the currency exchanged between
// sources, the plan, the registry and providers.
type Endpoint struct {
	// DNSName is the fully-qualified record name, e.g. "app.example.com".
	DNSName string
	// RecordType is one of the RecordType* constants.
	RecordType string
	// Targets are the record contents (IPs for A/AAAA, a hostname for CNAME,
	// the quoted value for TXT).
	Targets []string
	// RecordTTL is the desired TTL, or zero for the provider default.
	RecordTTL TTL
	// Labels carry controller metadata such as ownership; they are never
	// written to the provider as part of the record itself.
	Labels map[string]string
	// ProviderSpecific carries provider-only hints (see ProviderSpecific).
	ProviderSpecific ProviderSpecific
}

// New builds an endpoint with initialised maps.
func New(dnsName, recordType string, ttl TTL, targets ...string) *Endpoint {
	return &Endpoint{
		DNSName:          strings.TrimSuffix(dnsName, "."),
		RecordType:       strings.ToUpper(recordType),
		Targets:          targets,
		RecordTTL:        ttl,
		Labels:           map[string]string{},
		ProviderSpecific: ProviderSpecific{},
	}
}

// Key uniquely identifies the record a plan operates on: a DNS name may hold
// several record types, but only one set of records per (name, type).
// Comparison is case-insensitive because DNS names are.
func (e *Endpoint) Key() string {
	return e.RecordType + "/" + strings.ToLower(strings.TrimSuffix(e.DNSName, "."))
}

// Owner returns the owner id recorded on the endpoint, or "" if unmanaged.
func (e *Endpoint) Owner() string {
	if e.Labels == nil {
		return ""
	}
	return e.Labels[OwnerLabelKey]
}

// WithLabel returns the endpoint after setting a label, initialising the map if
// needed. It returns the receiver to allow chaining.
func (e *Endpoint) WithLabel(key, value string) *Endpoint {
	if e.Labels == nil {
		e.Labels = map[string]string{}
	}
	e.Labels[key] = value
	return e
}

// SameTargets reports whether two endpoints resolve to the same set of targets,
// order-independent.
func (e *Endpoint) SameTargets(other *Endpoint) bool {
	return equalStringSets(e.Targets, other.Targets)
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if !strings.EqualFold(as[i], bs[i]) {
			return false
		}
	}
	return true
}
