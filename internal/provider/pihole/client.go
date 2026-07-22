// Package pihole implements the Provider interface against a Pi-hole v6
// instance, managing its local DNS "hosts" entries (A/AAAA records). Pi-hole
// cannot store TXT records, so this provider is meant to run with the noop
// registry: pair it with a domain filter to scope what it manages.
package pihole

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// client handles authentication and requests against the Pi-hole API. A single
// session ID (SID) is reused across requests and transparently refreshed when
// it expires or is rejected.
type client struct {
	http     *http.Client
	baseURL  string
	password string

	mu        sync.Mutex
	sid       string
	expiresAt time.Time
}

func newClient(rawURL, password string, tlsInsecure bool) (*client, error) {
	u := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if u == "" {
		return nil, fmt.Errorf("pihole url must not be empty")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("pihole url %q must include a scheme (http:// or https://)", rawURL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsInsecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &client{
		http:     &http.Client{Timeout: 30 * time.Second, Transport: transport},
		baseURL:  u,
		password: password,
	}, nil
}

// ---- Authentication ----

type authResponse struct {
	Session struct {
		Valid    bool   `json:"valid"`
		SID      string `json:"sid"`
		Validity int    `json:"validity"` // seconds until the session expires
		Message  string `json:"message"`
	} `json:"session"`
}

// authenticate exchanges the password for a session and records when it will
// expire. A minute of slack is subtracted so a request never races the server's
// own expiry.
func (c *client) authenticate(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{"password": c.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/auth", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pihole auth request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pihole auth: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out authResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("pihole auth: decoding response: %w", err)
	}
	if !out.Session.Valid || out.Session.SID == "" {
		return fmt.Errorf("pihole auth: invalid password (%s)", out.Session.Message)
	}

	validity := max(time.Duration(out.Session.Validity)*time.Second, time.Minute)
	c.sid = out.Session.SID
	c.expiresAt = time.Now().Add(validity - time.Minute)
	return nil
}

// ensureSession authenticates if there is no live session.
func (c *client) ensureSession(ctx context.Context) error {
	if c.sid != "" && time.Now().Before(c.expiresAt) {
		return nil
	}
	return c.authenticate(ctx)
}

// ---- Requests ----

// do issues an authenticated request, reusing the SID via the X-FTL-SID header
// and re-authenticating once if the session was rejected. out may be nil.
func (c *client) do(ctx context.Context, method, path string, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureSession(ctx); err != nil {
		return err
	}

	resp, data, err := c.send(ctx, method, path)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The session expired earlier than advertised; refresh and retry once.
		c.sid = ""
		if err := c.ensureSession(ctx); err != nil {
			return err
		}
		if resp, data, err = c.send(ctx, method, path); err != nil {
			return err
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pihole %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("pihole %s %s: decoding response: %w", method, path, err)
		}
	}
	return nil
}

func (c *client) send(ctx context.Context, method, path string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-FTL-SID", c.sid)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("pihole %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}

// ---- Local DNS hosts ----

type hostsResponse struct {
	Config struct {
		DNS struct {
			Hosts []string `json:"hosts"`
		} `json:"dns"`
	} `json:"config"`
}

// getHosts returns the raw "<ip> <hostname>" entries from Pi-hole's local DNS.
func (c *client) getHosts(ctx context.Context) ([]string, error) {
	var out hostsResponse
	if err := c.do(ctx, http.MethodGet, "/api/config/dns/hosts", &out); err != nil {
		return nil, err
	}
	return out.Config.DNS.Hosts, nil
}

// addHost adds a "<ip> <hostname>" entry. The value is a single path segment,
// so the space becomes %20 (e.g. .../hosts/1.1.1.1%20teste.teste).
func (c *client) addHost(ctx context.Context, value string) error {
	return c.do(ctx, http.MethodPut, "/api/config/dns/hosts/"+url.PathEscape(value), nil)
}

// deleteHost removes a "<ip> <hostname>" entry.
func (c *client) deleteHost(ctx context.Context, value string) error {
	return c.do(ctx, http.MethodDelete, "/api/config/dns/hosts/"+url.PathEscape(value), nil)
}
