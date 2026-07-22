// Package registry implements ownership tracking on top of a provider using
// companion TXT records, the same provider-agnostic technique external-dns
// uses. For every managed record the registry writes a TXT record encoding the
// owning controller's id; on reconcile it reads those back to decide which
// records it is allowed to touch. This works even for providers that offer no
// per-record metadata of their own.
package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/ronaldmiranda/externaldns-controller/internal/endpoint"
	"github.com/ronaldmiranda/externaldns-controller/internal/plan"
	"github.com/ronaldmiranda/externaldns-controller/internal/provider"
)

// heritageValue mirrors the external-dns TXT format so records remain legible
// to operators familiar with it.
const heritage = "externaldns-controller"

// TXTRegistry wraps a provider, hiding ownership TXT records from callers and
// keeping them in sync as endpoints are created and deleted.
type TXTRegistry struct {
	provider provider.Provider
	ownerID  string
	prefix   string
}

// New returns a registry that owns records on behalf of ownerID. prefix is
// prepended to ownership TXT names to keep them out of the way (e.g.
// "externaldns-" turns app.example.com/A into TXT externaldns-a-app.example.com).
func New(p provider.Provider, ownerID, prefix string) *TXTRegistry {
	return &TXTRegistry{provider: p, ownerID: ownerID, prefix: prefix}
}

// Records returns the provider's real records (ownership TXT records removed),
// each annotated with the owner id read from its companion TXT record when one
// exists.
func (r *TXTRegistry) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	records, err := r.provider.Records(ctx)
	if err != nil {
		return nil, err
	}

	owners := map[string]string{} // record key -> owner id
	var real []*endpoint.Endpoint
	for _, rec := range records {
		if key, owner, ok := r.parseOwnership(rec); ok {
			owners[key] = owner
			continue // registry records are never exposed
		}
		real = append(real, rec)
	}

	for _, rec := range real {
		if owner, ok := owners[rec.Key()]; ok {
			rec.WithLabel(endpoint.OwnerLabelKey, owner)
		}
	}
	return real, nil
}

// ApplyChanges forwards the plan to the provider, adding the ownership TXT
// records that must accompany every create and delete so the registry stays
// consistent with the real records.
func (r *TXTRegistry) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	out := &plan.Changes{
		// Updates never change ownership, so their TXT records are left as-is.
		UpdateOld: changes.UpdateOld,
		UpdateNew: changes.UpdateNew,
	}
	for _, ep := range changes.Create {
		out.Create = append(out.Create, ep, r.txtFor(ep))
	}
	for _, ep := range changes.Delete {
		out.Delete = append(out.Delete, ep, r.txtFor(ep))
	}
	return r.provider.ApplyChanges(ctx, out)
}

// txtFor builds the ownership TXT record that shadows ep.
func (r *TXTRegistry) txtFor(ep *endpoint.Endpoint) *endpoint.Endpoint {
	name := r.txtName(ep)
	value := fmt.Sprintf("heritage=%s,%s/owner=%s", heritage, heritage, r.ownerID)
	return endpoint.New(name, endpoint.RecordTypeTXT, endpoint.TTLDefault, value)
}

// txtName is the DNS name of ep's ownership record: <prefix><type>-<name>.
func (r *TXTRegistry) txtName(ep *endpoint.Endpoint) string {
	return fmt.Sprintf("%s%s-%s", r.prefix, strings.ToLower(ep.RecordType), ep.DNSName)
}

// parseOwnership decodes a record back into the (record key, owner id) it
// tracks, or reports ok=false if it is not one of our ownership TXT records.
func (r *TXTRegistry) parseOwnership(rec *endpoint.Endpoint) (key, owner string, ok bool) {
	if rec.RecordType != endpoint.RecordTypeTXT {
		return "", "", false
	}
	name := strings.ToLower(strings.TrimSuffix(rec.DNSName, "."))
	if !strings.HasPrefix(name, r.prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, r.prefix)
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return "", "", false
	}
	recordType := strings.ToUpper(rest[:dash])
	dnsName := rest[dash+1:]
	key = recordType + "/" + dnsName

	for _, target := range rec.Targets {
		for _, field := range strings.Split(unquote(target), ",") {
			if v, found := strings.CutPrefix(strings.TrimSpace(field), heritage+"/owner="); found {
				owner = v
			}
		}
	}
	return key, owner, true
}

// unquote strips the surrounding quotes some providers keep on TXT values.
func unquote(s string) string {
	return strings.Trim(s, "\"")
}
