# Controller API discovery and preflight

Use these read-only HTTP endpoints (or the equivalent SchemaService and
ResourceService RPCs). The CLI currently has no `schema` subcommand and no
`ctl apply --dry-run` flag. Do not invent them or confuse offline `plan` with
provider preview.

## Connect to the correct gateway

Discover the intended controller's HTTPS gateway from its startup configuration,
service arguments, or logs. Its default is `https://127.0.0.1:9445`; the gRPC
endpoint defaults to `127.0.0.1:9444` and is **not** the HTTP gateway. Ports and
hosts can differ. Use an approved SSH forward if the gateway is loopback-only;
do not expose a new network listener just for preflight.

Set these environment variables from the selected CLI configuration and service
configuration, without printing credential contents:

- `OPENCTL_HTTP_URL`: reachable HTTPS gateway base URL, without `/ui/` or `/v1`.
- `OPENCTL_CA_FILE`: local copy of the controller CA; normally the CLI's
  `controller.caFile`, with home expansion for the CLI user.
- `OPENCTL_TOKEN_FILE`: local controller credential file; normally the CLI's
  `controller.tokenFile`. Use the least-privileged authorized credential.
- `OPENCTL_RESOURCE_JSON`: path to the concrete desired resource as a JSON object,
  **not** the outer `{"resource": ...}` request envelope.

Verify TLS using that CA; never use insecure certificate bypass as a deployment
fix. Keep tokens in protected files and read them inside the client, not via
command-line authorization headers. This Python 3 snippet uses only the standard
library. Run it locally with protected input/output; diagnostics and preview
fields may themselves contain sensitive desired values.

```python
import json
import os
from pathlib import Path
import ssl
from urllib import error, parse, request

base = os.environ["OPENCTL_HTTP_URL"].rstrip("/")
if parse.urlsplit(base).scheme != "https":
    raise SystemExit("Controller preflight requires HTTPS")
ca_file = Path(os.environ["OPENCTL_CA_FILE"]).expanduser()
token_file = Path(os.environ["OPENCTL_TOKEN_FILE"]).expanduser()
token = token_file.read_text().strip()
if not token:
    raise SystemExit("Controller credential file is empty")
tls = ssl.create_default_context(cafile=str(ca_file))


def read_api(path, payload=None):
    # Never turn this preflight helper into an Apply/Delete/action client.
    allowed = {
        "/v1/schemas",
        "/v1/schemas:get",
        "/v1/schemas:validate",
        "/v1/resources:dryRunApply",
    }
    if path not in allowed or (path == "/v1/schemas") != (payload is None):
        raise ValueError("Unsupported preflight request")
    body = None if payload is None else json.dumps(payload).encode()
    req = request.Request(
        base + path,
        data=body,
        method="GET" if payload is None else "POST",
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        },
    )
    try:
        with request.urlopen(req, context=tls, timeout=60) as response:
            return json.load(response)
    except error.HTTPError as exc:
        # Do not echo the request body, credential, or potentially secret error.
        raise SystemExit(f"Controller preflight HTTP {exc.code} at {path}") from None


schemas = read_api("/v1/schemas")
resource = json.loads(Path(os.environ["OPENCTL_RESOURCE_JSON"]).expanduser().read_text())
kind = {"apiVersion": resource["apiVersion"], "kind": resource["kind"]}
schema = read_api("/v1/schemas:get", kind)
# Inspect schemas["schemas"] and schema["cueSource"] locally to author the intent.
validation = read_api("/v1/schemas:validate", {"resource": resource})
if validation.get("errors"):
    raise SystemExit("Schema validation failed; inspect validation['errors'] locally")
preview = read_api("/v1/resources:dryRunApply", {"resource": resource})
if preview.get("validationErrors"):
    raise SystemExit("Preview rejected the intent; inspect preview['validationErrors'] locally")
print(json.dumps(preview, indent=2))
```

During initial discovery, use the `read_api` function with just the schemas
calls; no resource is needed for `GET /v1/schemas`. Schemas can include kinds
whose provider is not configured. Treat a provider-not-configured response as a
setup blocker, not a reason to use another controller or a direct mutation path.
The example client deliberately stops on errors and has no Apply/Delete method.

## Interpret responses, not just HTTP status

| Request | Body | Required inspection |
| --- | --- | --- |
| `GET /v1/schemas` | None | `schemas[]`: API version, kind, provider, advanced/owner hints |
| `POST /v1/schemas:get` | `{"apiVersion":"proxmox.openctl.io/v1","kind":"VirtualMachine"}` | `cueSource`: current supported fields and constraints |
| `POST /v1/schemas:validate` | `{"resource": RESOURCE_OBJECT}` | Empty/absent `errors`; HTTP 200 alone is not validation success |
| `POST /v1/resources:dryRunApply` | `{"resource": RESOURCE_OBJECT}` | Empty/absent `validationErrors`, then `diff`, `children`, `requiredGates`, `summary` |

- Proto JSON fields use lower camel case (`validationErrors`, `requiredGates`).
- Missing reference outputs can prevent preview. Create only approved upstream
  dependencies first, verify their outputs, then preview the dependent resource.
- An empty atomic diff, including a `no-op` summary on first apply, is not proof
  of physical existence, no live drift, or a safe replacement. Preview compares
  saved desired specs; composite providers can additionally return child plans.
- Preview cannot guarantee future API access, quotas, capacity, cloud-init
  success, or absence of concurrent changes. Verify those independently.
- Review nonempty `requiredGates`; never translate them into automatic destructive
  flags. Have the user authorize the specific removal/data/quorum impact first.

## Concrete VM declaration shape

This example is a **shape**, not a discovered deployment target. Choose the
actual name, context, node, image, storage, network, guest access, and power
state from the user's request and read-only discovery. Do not apply it unchanged.
Memory is MiB; disk sizes include a unit. A template must already exist on the
selected endpoint. Guest SSH keys, when needed, belong under `spec.cloudInit`.

```json
{
  "apiVersion": "proxmox.openctl.io/v1",
  "kind": "VirtualMachine",
  "metadata": {"name": "example-web"},
  "spec": {
    "context": "lab",
    "node": "pve1",
    "template": {"name": "ubuntu-24.04-cloudinit"},
    "cpu": {"cores": 2, "sockets": 1},
    "memory": {"size": 4096},
    "disks": [{"name": "scsi0", "storage": "local-lvm", "size": "32G"}],
    "networks": [{"name": "net0", "bridge": "vmbr0", "model": "virtio"}],
    "startOnCreate": false
  }
}
```

JSON is a YAML subset. To avoid a YAML-parser dependency, the **same concrete
JSON object** can be saved with a `.yaml` filename: use that file as
`OPENCTL_RESOURCE_JSON` during preflight and as `ctl apply -f` input afterward.
For ordinary YAML or CUE, produce a faithful concrete JSON representation for
preflight using available parsing/evaluation tools; do not hand-change fields
between validation, preview, and apply. CUE imports/values must be resolved, not
sent as CUE source in a JSON string. If the intent changes, repeat preflight.
