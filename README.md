# externaldns-controller

[![Container Build](https://github.com/ronaldmiranda/docker-externaldns-controller/actions/workflows/container_build.yml/badge.svg)](https://github.com/ronaldmiranda/docker-externaldns-controller/actions/workflows/container_build.yml)
[![Docker Hub](https://img.shields.io/docker/v/ronaldmiranda/docker-externaldns-controller?label=docker%20hub)](https://hub.docker.com/r/ronaldmiranda/docker-externaldns-controller)

*Leia em [português](README.pt-BR.md).*

A Go controller that watches Docker containers and keeps a **cloud DNS
provider** in sync with the records declared in their labels. Inspired by
[external-dns](https://github.com/kubernetes-sigs/external-dns), but for Docker
instead of Kubernetes.

Add a couple of labels to any service and the matching DNS record appears; stop
the container and it is cleaned up:

```yaml
services:
  app:
    image: nginx
    labels:
      externaldns.hostname: app.example.com
      externaldns.target: "203.0.113.10"
```

The provider is chosen at runtime (`--provider` / `PROVIDER`), so the same
binary targets Cloudflare today and any backend added later — the controller
core never changes.

## How it works

```
Docker labels ──▶  Source  ──▶ Plan ──▶ Registry  ──▶  Provider   ──▶ Cloud DNS
               (externaldns    (diff)  (ownership)   (Cloudflare,
                + Traefik)                             Pi-hole)
```

- **Source** — lists running containers and builds the desired set of DNS
  endpoints from their labels: the native `externaldns.*` scheme and,
  optionally, [Traefik router rules](#traefik-labels). It also watches Docker
  events (`start`, `stop`, `die`, ...) with debouncing, plus a periodic resync
  (default 5 min).
- **Plan** — diffs desired endpoints against what the provider already holds
  and produces the minimal create/update/delete set.
- **Registry** — decides which records the controller is allowed to change. The
  default `txt` registry writes a companion **TXT ownership record** per managed
  record (external-dns style), so it **only ever touches records it owns** —
  manual records and records owned by another instance are reported as conflicts
  and left alone. Providers that cannot store TXT (e.g. Pi-hole) use the `noop`
  registry instead. See [Registries](#registries-ownership).
- **Provider** — translates the plan into API calls. Cloudflare and Pi-hole ship
  today; adding another means implementing a small interface (see
  [Adding a provider](#adding-a-provider)).

Every stage is pluggable, so a new source (Podman), registry or provider drops
in without touching the others.

## Labels

| Label | Required | Description |
|---|---|---|
| `externaldns.hostname` | yes | The DNS name, e.g. `app.example.com` |
| `externaldns.type` | no | `A` (default), `AAAA`, `CNAME` or `TXT` |
| `externaldns.target` | yes* | Record content. Comma-separate for multiple targets (e.g. two A records). `*`optional for `A`/`AAAA` when `--default-target` is set |
| `externaldns.ttl` | no | TTL in seconds; omit for the provider default |
| `externaldns.proxied` | no | Cloudflare only: `true`/`false` to route through its proxy |
| `externaldns.exclude` | no | `true` opts the whole container out of DNS management (native and Traefik) |

### Multiple records on one container

Use an index segment:

```yaml
labels:
  externaldns.0.hostname: app.example.com
  externaldns.0.target: "203.0.113.10"
  externaldns.1.hostname: www.example.com
  externaldns.1.type: CNAME
  externaldns.1.target: app.example.com
```

### Traefik labels

If you already label your services for [Traefik](https://traefik.io), you get
DNS for free — no `externaldns.*` labels needed. The controller reads the
hostnames from Traefik router rules (enabled by default; disable with
`--traefik=false`):

```yaml
labels:
  traefik.enable: "true"
  traefik.http.routers.app.rule: Host(`app.example.com`)
```

- Both `Host(...)` (HTTP routers) and `HostSNI(...)` (TCP routers) are read;
  `Host(`a`, `b`)` and `Host(`a`) || Host(`b`)` yield multiple records.
  `HostRegexp(...)` is ignored — a regex has no concrete name.
- `traefik.enable: "false"` opts a container out, mirroring Traefik itself.
- To skip DNS for a service **while keeping Traefik routing**, add
  `externaldns.exclude: "true"` — the controller ignores the container (and
  removes any record it previously owned for it).
- Traefik labels say *what* the hostname is, not *where* it points. The record
  is therefore an `A` record to `--default-target` (typically the host IP where
  Traefik listens). Set it globally:

  ```bash
  --default-target 203.0.113.10   # or DEFAULT_TARGET=203.0.113.10
  ```

- To override the record shape for one container, add the non-indexed
  `externaldns.*` keys alongside the Traefik labels — e.g. point every Traefik
  host on this container at a proxied CNAME:

  ```yaml
  labels:
    traefik.http.routers.app.rule: Host(`app.example.com`)
    externaldns.type: CNAME
    externaldns.target: tunnel.example.net
    externaldns.proxied: "true"
  ```

When a container carries both a native `externaldns.hostname` record and a
Traefik rule for the same name, the explicit native declaration wins.

## Configuration

Every flag has an environment-variable equivalent. Flags win over the
environment, which wins over the defaults.

| Flag | Env | Default | Description |
|---|---|---|---|
| `--provider` | `PROVIDER` | `cloudflare` | DNS backend to manage (`cloudflare`, `pihole`) |
| `--registry` | `REGISTRY` | *(per-provider)* | Ownership tracking: `txt` or `noop` |
| `--label-prefix` | `LABEL_PREFIX` | `externaldns` | Container label namespace |
| `--owner-id` | `OWNER_ID` | machine hostname | Identity written to ownership TXT records |
| `--txt-prefix` | `TXT_PREFIX` | `externaldns-` | Prefix for ownership TXT record names |
| `--domain-filter` | `DOMAIN_FILTER` | *(all visible)* | Comma-separated domains/zones to manage |
| `--default-target` | `DEFAULT_TARGET` | *(none)* | Fallback content for `A`/`AAAA` records without a target |
| `--traefik` | `ENABLE_TRAEFIK` | `true` | Derive records from Traefik router `Host(...)` labels |
| `--resync-interval` | `RESYNC_INTERVAL` | `5m` | Full reconcile cadence |
| `--debounce-interval` | `DEBOUNCE_INTERVAL` | `2s` | Event batching window |
| `--dry-run` | `DRY_RUN` | `false` | Log changes without applying them |
| `--log-level` | `LOG_LEVEL` | `info` | `info` or `debug` |
| `--cf-api-token` | `CLOUDFLARE_API_TOKEN` | — | Cloudflare token (`Zone > DNS > Edit`) |
| `--pihole-url` | `PIHOLE_URL` | — | Pi-hole base URL, e.g. `http://pi.hole` |
| `--pihole-password` | `PIHOLE_PASSWORD` | — | Pi-hole web/API password |
| `--pihole-tls-insecure` | `PIHOLE_TLS_INSECURE` | `false` | Skip TLS verification (self-signed certs) |

### Cloudflare token

Create an API token with **Zone → DNS → Edit** on the zones you want to manage.
No account-level permission is needed. Scope the token to specific zones and the
controller stays within them; use `--domain-filter` to narrow further.

### Pi-hole

Manages **Pi-hole v6 local DNS** (`Settings → Local DNS records`), i.e. the
`<ip> <hostname>` entries — so `A` and `AAAA` records. The controller
authenticates with the web/API password (`POST /api/auth`), reuses the returned
session id via the `X-FTL-SID` header, and refreshes it automatically when it
expires. Record types Pi-hole cannot store as hosts (`CNAME`, `TXT`) are skipped
with a warning.

```bash
PIHOLE_URL=http://pi.hole PIHOLE_PASSWORD=... \
  ./externaldns-controller --provider pihole --domain-filter home.example.com --dry-run
```

> **Ownership on Pi-hole.** Pi-hole has nowhere to store the TXT ownership
> records the default registry uses, so the Pi-hole provider defaults to the
> **`noop` registry**: it manages *every* local DNS entry in scope. **Always set
> `--domain-filter`** so it only touches your managed names — otherwise
> hand-made entries outside the desired set would be removed.

## Registries (ownership)

How the controller decides which records it may modify or delete:

- **`txt`** (default for Cloudflare) — writes a companion TXT record per managed
  record (external-dns style). Only records it created are ever changed; manual
  records and records owned by another instance are left alone.
- **`noop`** (default for Pi-hole) — no ownership marker; the controller manages
  everything the provider reports within scope. Pair it with `--domain-filter`.

Override the default with `--registry`.

## Running

With Docker Compose — [`docker-compose.yml`](docker-compose.yml) for Cloudflare,
[`docker-compose.pihole.yml`](docker-compose.pihole.yml) for Pi-hole:

```bash
export CLOUDFLARE_API_TOKEN=...
docker compose up -d
```

Or standalone:

```bash
go build -o externaldns-controller ./cmd/externaldns-controller
CLOUDFLARE_API_TOKEN=... ./externaldns-controller --dry-run
```

Start with `--dry-run` to see exactly which records would be created before
letting the controller write anything.

## Adding a provider

1. Create `internal/provider/<name>/` implementing the two-method
   [`provider.Provider`](internal/provider/provider.go) interface:

   ```go
   Records(ctx) ([]*endpoint.Endpoint, error)
   ApplyChanges(ctx, *plan.Changes) error
   ```

2. Register it in the factory switch in
   [`cmd/externaldns-controller/main.go`](cmd/externaldns-controller/main.go).
   If the backend cannot store TXT records, default it to the `noop` registry in
   `Config.RegistryKind` (as Pi-hole does).

Nothing else — sources, planning and registries are provider-agnostic. Podman is
a natural next source (it speaks the Docker API), and Route53 / DigitalOcean are
natural next providers.

## Development

```bash
go test ./...                         # unit tests
go test -tags integration ./...       # requires a running Docker daemon
go vet ./... && gofmt -l .
```

## License

[MIT](LICENSE)
