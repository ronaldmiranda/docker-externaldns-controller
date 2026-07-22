# externaldns-controller

[![Container Build](https://github.com/ronaldmiranda/docker-externaldns-controller/actions/workflows/container_build.yml/badge.svg)](https://github.com/ronaldmiranda/docker-externaldns-controller/actions/workflows/container_build.yml)
[![Docker Hub](https://img.shields.io/docker/v/ronaldmiranda/docker-externaldns-controller?label=docker%20hub)](https://hub.docker.com/r/ronaldmiranda/docker-externaldns-controller)

*Read in [English](README.md).*

Um controller em Go que observa containers Docker e mantém um **provedor de DNS
na nuvem** sincronizado com os registros declarados nas labels deles. Inspirado
no [external-dns](https://github.com/kubernetes-sigs/external-dns), mas para
Docker em vez de Kubernetes.

Adicione duas labels a qualquer serviço e o registro DNS correspondente
aparece; pare o container e ele é removido:

```yaml
services:
  app:
    image: nginx
    labels:
      externaldns.hostname: app.example.com
      externaldns.target: "203.0.113.10"
```

O provedor é escolhido em tempo de execução (`--provider` / `PROVIDER`), então o
mesmo binário atende à Cloudflare hoje e a qualquer backend adicionado depois —
o núcleo do controller não muda.

## Como funciona

```
Labels Docker ──▶  Source  ──▶ Plan ──▶ Registry ──▶  Provider   ──▶ DNS na nuvem
              (externaldns    (diff)   (posse)     (Cloudflare,
               + Traefik)                            Pi-hole)
```

- **Source** — lista os containers em execução e monta o conjunto desejado de
  registros a partir das labels: o esquema nativo `externaldns.*` e,
  opcionalmente, as [regras de router do Traefik](#labels-do-traefik). Também
  observa os eventos do Docker (`start`, `stop`, `die`, ...) com debounce, além
  de um resync periódico (padrão: 5 min).
- **Plan** — compara o desejado com o que o provedor já possui e gera o conjunto
  mínimo de operações (criar/atualizar/remover).
- **Registry** — decide quais registros o controller pode alterar. O registry
  padrão `txt` grava um **registro TXT de posse** por registro gerenciado (estilo
  external-dns), então **só mexe no que é dele** — registros manuais ou de outra
  instância viram conflito e ficam intactos. Provedores que não guardam TXT (ex.
  Pi-hole) usam o registry `noop`. Ver [Registries](#registries-posse).
- **Provider** — traduz o plano em chamadas de API. Cloudflare e Pi-hole já vêm
  prontos; adicionar outro significa implementar uma interface pequena (ver
  [Adicionando um provedor](#adicionando-um-provedor)).

Cada etapa é plugável, então uma nova source (Podman), registry ou provedor
entra sem mexer nas outras.

## Labels

| Label | Obrigatória | Descrição |
|---|---|---|
| `externaldns.hostname` | sim | O nome DNS, ex. `app.example.com` |
| `externaldns.type` | não | `A` (padrão), `AAAA`, `CNAME` ou `TXT` |
| `externaldns.target` | sim* | Conteúdo do registro. Separe por vírgula para múltiplos alvos (ex. dois registros A). `*`opcional para `A`/`AAAA` quando `--default-target` estiver definido |
| `externaldns.ttl` | não | TTL em segundos; omita para o padrão do provedor |
| `externaldns.proxied` | não | Só Cloudflare: `true`/`false` para passar pelo proxy |

### Vários registros em um container

Use um segmento de índice:

```yaml
labels:
  externaldns.0.hostname: app.example.com
  externaldns.0.target: "203.0.113.10"
  externaldns.1.hostname: www.example.com
  externaldns.1.type: CNAME
  externaldns.1.target: app.example.com
```

### Labels do Traefik

Se você já rotula seus serviços para o [Traefik](https://traefik.io), ganha DNS
de graça — sem precisar de labels `externaldns.*`. O controller lê os hostnames
das regras de router do Traefik (ativo por padrão; desative com
`--traefik=false`):

```yaml
labels:
  traefik.enable: "true"
  traefik.http.routers.app.rule: Host(`app.example.com`)
```

- Lê tanto `Host(...)` (routers HTTP) quanto `HostSNI(...)` (routers TCP);
  `Host(`a`, `b`)` e `Host(`a`) || Host(`b`)` geram vários registros.
  `HostRegexp(...)` é ignorado — uma regex não tem nome concreto.
- `traefik.enable: "false"` exclui o container, igual ao próprio Traefik.
- As labels do Traefik dizem *qual* é o hostname, não *para onde* ele aponta.
  Por isso o registro é um `A` apontando para `--default-target` (tipicamente o
  IP do host onde o Traefik escuta). Defina globalmente:

  ```bash
  --default-target 203.0.113.10   # ou DEFAULT_TARGET=203.0.113.10
  ```

- Para mudar o formato do registro em um container, adicione as chaves
  `externaldns.*` sem índice junto das labels do Traefik — ex. apontar todos os
  hosts Traefik deste container para um CNAME com proxy:

  ```yaml
  labels:
    traefik.http.routers.app.rule: Host(`app.example.com`)
    externaldns.type: CNAME
    externaldns.target: tunnel.example.net
    externaldns.proxied: "true"
  ```

Quando um container tem tanto um registro nativo `externaldns.hostname` quanto
uma regra Traefik para o mesmo nome, a declaração nativa explícita prevalece.

## Configuração

Toda flag tem uma variável de ambiente equivalente. As flags têm prioridade
sobre o ambiente, que tem prioridade sobre os padrões.

| Flag | Env | Padrão | Descrição |
|---|---|---|---|
| `--provider` | `PROVIDER` | `cloudflare` | Backend de DNS a gerenciar (`cloudflare`, `pihole`) |
| `--registry` | `REGISTRY` | *(por provedor)* | Rastreio de posse: `txt` ou `noop` |
| `--label-prefix` | `LABEL_PREFIX` | `externaldns` | Namespace das labels |
| `--owner-id` | `OWNER_ID` | hostname da máquina | Identidade gravada nos TXT de posse |
| `--txt-prefix` | `TXT_PREFIX` | `externaldns-` | Prefixo dos nomes dos TXT de posse |
| `--domain-filter` | `DOMAIN_FILTER` | *(todos visíveis)* | Domínios/zonas a gerenciar (separados por vírgula) |
| `--default-target` | `DEFAULT_TARGET` | *(nenhum)* | Conteúdo padrão para `A`/`AAAA` sem target |
| `--traefik` | `ENABLE_TRAEFIK` | `true` | Deriva registros das labels `Host(...)` do Traefik |
| `--resync-interval` | `RESYNC_INTERVAL` | `5m` | Cadência do reconcile completo |
| `--debounce-interval` | `DEBOUNCE_INTERVAL` | `2s` | Janela de agrupamento de eventos |
| `--dry-run` | `DRY_RUN` | `false` | Loga as mudanças sem aplicá-las |
| `--log-level` | `LOG_LEVEL` | `info` | `info` ou `debug` |
| `--cf-api-token` | `CLOUDFLARE_API_TOKEN` | — | Token Cloudflare (`Zone > DNS > Edit`) |
| `--pihole-url` | `PIHOLE_URL` | — | URL base do Pi-hole, ex. `http://pi.hole` |
| `--pihole-password` | `PIHOLE_PASSWORD` | — | Senha web/API do Pi-hole |
| `--pihole-tls-insecure` | `PIHOLE_TLS_INSECURE` | `false` | Ignora verificação TLS (certificados self-signed) |

### Token da Cloudflare

Crie um token de API com **Zone → DNS → Edit** nas zonas que quer gerenciar. Não
é preciso permissão de conta. Restrinja o token a zonas específicas e o
controller ficará dentro delas; use `--domain-filter` para restringir mais.

### Pi-hole

Gerencia o **DNS local do Pi-hole v6** (`Settings → Local DNS records`), ou seja,
as entradas `<ip> <hostname>` — portanto registros `A` e `AAAA`. O controller
autentica com a senha web/API (`POST /api/auth`), reaproveita o session id
retornado via header `X-FTL-SID` e o renova automaticamente quando expira. Tipos
que o Pi-hole não guarda como hosts (`CNAME`, `TXT`) são pulados com um aviso.

```bash
PIHOLE_URL=http://pi.hole PIHOLE_PASSWORD=... \
  ./externaldns-controller --provider pihole --domain-filter home.example.com --dry-run
```

> **Posse no Pi-hole.** O Pi-hole não tem onde guardar os registros TXT de posse
> que o registry padrão usa, então o provedor Pi-hole usa por padrão o **registry
> `noop`**: ele gerencia *todas* as entradas de DNS local no escopo. **Sempre
> defina `--domain-filter`** para que ele só toque nos seus nomes gerenciados —
> caso contrário, entradas feitas à mão fora do conjunto desejado seriam removidas.

## Registries (posse)

Como o controller decide quais registros pode alterar ou remover:

- **`txt`** (padrão da Cloudflare) — grava um registro TXT companheiro por
  registro gerenciado (estilo external-dns). Só mexe no que criou; registros
  manuais e de outras instâncias ficam intactos.
- **`noop`** (padrão do Pi-hole) — sem marca de posse; o controller gerencia tudo
  que o provedor reporta no escopo. Use junto com `--domain-filter`.

Sobrescreva o padrão com `--registry`.

## Executando

Com Docker Compose — [`docker-compose.yml`](docker-compose.yml) para Cloudflare,
[`docker-compose.pihole.yml`](docker-compose.pihole.yml) para Pi-hole:

```bash
export CLOUDFLARE_API_TOKEN=...
docker compose up -d
```

Ou standalone:

```bash
go build -o externaldns-controller ./cmd/externaldns-controller
CLOUDFLARE_API_TOKEN=... ./externaldns-controller --dry-run
```

Comece com `--dry-run` para ver exatamente quais registros seriam criados antes
de deixar o controller escrever qualquer coisa.

## Adicionando um provedor

1. Crie `internal/provider/<nome>/` implementando a interface de dois métodos
   [`provider.Provider`](internal/provider/provider.go):

   ```go
   Records(ctx) ([]*endpoint.Endpoint, error)
   ApplyChanges(ctx, *plan.Changes) error
   ```

2. Registre-o no switch da factory em
   [`cmd/externaldns-controller/main.go`](cmd/externaldns-controller/main.go).
   Se o backend não guarda registros TXT, defina o registry `noop` como padrão
   dele em `Config.RegistryKind` (como o Pi-hole faz).

Nada além disso — source, planejamento e registries são agnósticos ao provedor.
O Podman é uma próxima source natural (fala a API do Docker), e Route53 /
DigitalOcean são próximos provedores naturais.

## Desenvolvimento

```bash
go test ./...                         # testes unitários
go test -tags integration ./...       # requer um daemon Docker rodando
go vet ./... && gofmt -l .
```

## Licença

[MIT](LICENSE)
