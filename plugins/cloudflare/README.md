# openctl-cloudflare

A native [openctl](../../README.md) provider plugin for **Cloudflare** — manage
DNS records and Cloudflare Tunnels as declarative openctl resources.

It speaks openctl's v2 plugin protocol (`pkg/pluginproto`) over stdio and talks
to the Cloudflare REST API v4 with a hand-rolled, dependency-free client. It is
**stateful**: Cloudflare-assigned IDs (record IDs, tunnel IDs) are persisted
through openctl's `provider_state` store, so the controller owns the full
create → update → delete lifecycle by ID.

## Kinds

`cloudflare.openctl.io/v1`

| Kind | Description |
|------|-------------|
| `DNSRecord` | A DNS record in a zone (A, AAAA, CNAME, TXT, MX, …). |
| `Tunnel` | A remotely-managed Cloudflare Tunnel (`cloudflared`), with ingress rules. |

## Setup

### 1. API token

Create a scoped Cloudflare API token (My Profile → API Tokens) with, at minimum:
- **Zone → DNS → Edit** (for `DNSRecord`)
- **Account → Cloudflare Tunnel → Edit** (for `Tunnel`)

Store it in a `0600` file so it never lands in config or git:

```sh
install -m600 /dev/null ~/.openctl/cloudflare.token
printf '%s' 'YOUR_CLOUDFLARE_API_TOKEN' > ~/.openctl/cloudflare.token
```

### 2. Build + install the plugin

```sh
make build-plugin-cloudflare      # -> bin/openctl-cloudflare
make install-plugins              # copies it to ~/.openctl/plugins/
```

### 3. Configure the provider

In `~/.openctl/config.yaml`:

```yaml
providers:
  cloudflare:
    command: openctl-cloudflare
    args: [plugin-serve]
    contexts:
      prod: { credentials: cf }
    credentials:
      cf: { tokenSecretFile: ~/.openctl/cloudflare.token }
    defaults:
      zoneId: <zone-id>       # default zone for DNSRecord + `list`
      accountId: <account-id> # default account for Tunnel + `list`
```

The token is read from the file into the provider's `Configure` bag; the plugin
never sees the path. `defaults.zoneId` / `defaults.accountId` are used when a
manifest omits `spec.zoneId` / `spec.accountId`, and are required for `list`.

## Examples

### DNS record

```yaml
apiVersion: cloudflare.openctl.io/v1
kind: DNSRecord
metadata:
  name: www
spec:
  # zoneId: <zone-id>     # optional; falls back to provider defaults.zoneId
  type: A
  name: www.example.com
  content: 203.0.113.10
  proxied: true
  ttl: 1                  # 1 = automatic
```

### Tunnel

The Cloudflare tunnel is named after `metadata.name`. A catch-all ingress rule
is appended automatically when the last rule is host-scoped.

```yaml
apiVersion: cloudflare.openctl.io/v1
kind: Tunnel
metadata:
  name: home
spec:
  # accountId: <account-id>   # optional; falls back to defaults.accountId
  ingress:
    - hostname: app.example.com
      service: http://localhost:8080
    - hostname: ssh.example.com
      service: ssh://localhost:22
```

Fetch the run token (for `cloudflared tunnel run --token …`) via the **get-token**
action — it is returned as a downloadable payload and is never written to the
resource's status or the git-synced state mirror:

```sh
openctl ctl action --api-version cloudflare.openctl.io/v1 --kind Tunnel \
  --name home --action get-token
```

### Routing a hostname to the tunnel (no manual id copying)

A tunnel serves a hostname via a proxied `CNAME` → `<tunnel-id>.cfargotunnel.com`.
The Tunnel exposes that ready-to-use target at `status.cnameTarget`, so a
`DNSRecord` can `$ref` it directly — you never copy the tunnel id by hand, and
openctl applies the tunnel first (the ref creates the dependency edge):

```yaml
apiVersion: cloudflare.openctl.io/v1
kind: DNSRecord
metadata:
  name: app
spec:
  type: CNAME
  name: app.example.com
  proxied: true
  content:
    $ref:
      apiVersion: cloudflare.openctl.io/v1
      kind: Tunnel
      name: home
      field: status.cnameTarget
```

## Notes / limitations (MVP)

- `list` is a live inventory of the default zone/account, independent of
  manifest names (DNS records are keyed by their stable Cloudflare ID).
- Tunnel ingress is pushed on apply but not yet round-tripped into observed
  state for drift, so re-applying an unchanged tunnel re-PUTs its config.
- Out-of-band deletes are self-healed: applying a record/tunnel whose stored ID
  no longer exists recreates it.
