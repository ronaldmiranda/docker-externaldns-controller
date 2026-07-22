// Package cloudflare implements the Provider interface against the Cloudflare
// v4 API: it lists and reconciles DNS records within the zones a scoped API
// token can see. It is the reference provider; new providers live in sibling
// packages and implement the same interface.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://api.cloudflare.com/client/v4"

// client is the thin HTTP layer shared by the provider. It caches the zone list
// for the lifetime of the process (zones rarely change; restart to pick up new
// ones).
type client struct {
	http  *http.Client
	token string

	zonesMu sync.Mutex
	zones   []Zone
}

func newClient(token string) *client {
	return &client{
		http:  &http.Client{Timeout: 30 * time.Second},
		token: token,
	}
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var env apiEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%s %s: HTTP %d: unparseable response: %w", method, path, resp.StatusCode, err)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d: %s", e.Code, e.Message))
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.Join(msgs, "; "))
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("%s %s: decoding result: %w", method, path, err)
		}
	}
	return nil
}

// ---- Zones ----

// Zone is a Cloudflare DNS zone.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *client) listZones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; ; page++ {
		var zones []Zone
		path := fmt.Sprintf("/zones?per_page=50&page=%d", page)
		if err := c.do(ctx, http.MethodGet, path, nil, &zones); err != nil {
			return nil, err
		}
		all = append(all, zones...)
		if len(zones) < 50 {
			return all, nil
		}
	}
}

// Zones returns all zones visible to the token, cached after the first call.
func (c *client) Zones(ctx context.Context) ([]Zone, error) {
	c.zonesMu.Lock()
	defer c.zonesMu.Unlock()
	if c.zones == nil {
		zones, err := c.listZones(ctx)
		if err != nil {
			return nil, err
		}
		c.zones = zones
	}
	return c.zones, nil
}

// zoneForHostname finds the zone whose name is the longest suffix of hostname.
func (c *client) zoneForHostname(ctx context.Context, hostname string) (*Zone, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, err
	}

	host := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(hostname, "*."), "."))
	var best *Zone
	for i := range zones {
		z := &zones[i]
		zn := strings.ToLower(z.Name)
		if host == zn || strings.HasSuffix(host, "."+zn) {
			if best == nil || len(z.Name) > len(best.Name) {
				best = z
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no Cloudflare zone found for hostname %q (token may lack zone access)", hostname)
	}
	return best, nil
}

// ---- DNS records ----

// dnsRecord mirrors the fields of the Cloudflare DNS record API this provider
// reads and writes.
type dnsRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl,omitempty"`
}

func (c *client) listDNSRecords(ctx context.Context, zoneID string, params map[string]string) ([]dnsRecord, error) {
	q := url.Values{"per_page": {"100"}}
	for k, v := range params {
		q.Set(k, v)
	}
	var records []dnsRecord
	path := fmt.Sprintf("/zones/%s/dns_records?%s", zoneID, q.Encode())
	if err := c.do(ctx, http.MethodGet, path, nil, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (c *client) createDNSRecord(ctx context.Context, zoneID string, rec dnsRecord) error {
	path := fmt.Sprintf("/zones/%s/dns_records", zoneID)
	return c.do(ctx, http.MethodPost, path, rec, nil)
}

func (c *client) updateDNSRecord(ctx context.Context, zoneID string, rec dnsRecord) error {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, rec.ID)
	return c.do(ctx, http.MethodPut, path, rec, nil)
}

func (c *client) deleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
