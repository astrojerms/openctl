# OIDC authentication — design proposal

**Status:** **backend IMPLEMENTED** (2026-07-07) — `auth.oidc` config block,
`internal/controller/auth/oidc.go` (authenticator), `internal/controller/
server/oidc_http.go` (login+callback routes), wired into the gateway; tested
against a fake IdP (`oidc_test.go`). The UI "Log in with SSO" button is also
shipped (login page probes `/auth/oidc/enabled`). Remaining: real-IdP
validation. The design below is what shipped.
**Author:** autonomous session, 2026-07-05.
**Roadmap item:** "Multi-user auth → *Next — OIDC: external IdP → claims →
role (the last big auth slice)*" (ROADMAP.md, Future goals). Direction:
`docs/direction.md` §"Multi-user auth (OIDC / RBAC)" — real, downstream of a
shared deployment creating a real second user.

This designs OIDC login as a **new front door that mints an existing
session** — it reuses the shipped RBAC spine, session store, and cookie
machinery wholesale, adding only the browser ↔ IdP handshake and a
claims → role mapping. It is written for sign-off before implementation; the
implementation (and its integration test against a real IdP) is a follow-on.

## What already exists (the foundations OIDC plugs into)

The auth stack is built and RBAC is live for token callers:

- **Role model** (`auth/principal.go`): `viewer ⊂ editor ⊂ admin`, rank-ordered,
  with `Role.AtLeast(min)`. `Principal{UserID, Role, Root}` is injected into
  every request context; `ResourceService` enforces editor+ for mutations,
  viewer+ for reads.
- **Token → Principal resolution** (`auth/auth.go` `check`): tries the root
  token, then named users (`users.yaml`), then the **sessions** table
  (sha256 lookup). OIDC adds no new branch here — it produces sessions, which
  this path already resolves.
- **Session store** (`auth/sessions.go`): `SessionStore.Create(ctx, userID,
  displayName, role, ttl)` mints a session row (sha256(token) stored, raw
  token returned), `DefaultSessionTTL` 7d. Sessions carry `user_id` + `role`
  (migration 0010).
- **Login → cookie** (`server/session.go` `Login`): today reads the caller's
  bearer-token principal and mints a session cookie carrying that role. OIDC
  is the same act, with the principal sourced from IdP claims instead of a
  pre-shared token.
- **HTTP gateway**: grpc-gateway on `127.0.0.1:9445` over HTTP/2+TLS, already
  serving the UI at `/ui/` and setting the `openctl_session` cookie
  (HttpOnly, Secure, SameSite=Strict).

**So the new surface is small:** two HTTP endpoints (login redirect +
callback), an OIDC config block, a claims→role mapper, and the token-exchange
plumbing. Everything downstream (RBAC, session lookup, cookie, WhoAmI) is
untouched.

## Proposed model

**Authorization Code flow with PKCE**, browser-first, discovery-based.

1. **`GET /auth/oidc/login`** — the UI's "Log in with SSO" button hits this.
   The server generates `state` + PKCE `code_verifier`, stashes them in a
   short-lived, HttpOnly cookie (or the sessions DB), and 302-redirects to the
   IdP's authorization endpoint (learned via OIDC discovery) with
   `scope=openid profile email <role-claim-scope>`.
2. **IdP authenticates the user** and redirects back to
   **`GET /auth/oidc/callback?code=…&state=…`**.
3. **Callback handler**: verify `state`; exchange `code` + `code_verifier` for
   tokens at the token endpoint; **verify the ID token** signature against the
   IdP's JWKS (issuer + audience + expiry checked); extract claims.
4. **Map claims → role** (see decision 2). Resolve `{UserID, Role}`.
5. **Mint a session** via the existing `SessionStore.Create(userID,
   displayName, role, ttl)`, set the `openctl_session` cookie exactly as
   `Login` does today, and redirect to `/ui/`.

From step 5 on, the caller is indistinguishable from a bearer-token login:
same cookie, same session lookup, same RBAC. OIDC is purely an
identity-source addition.

### Config surface (`config.yaml`, `auth.oidc` block)

```yaml
auth:
  oidc:
    enabled: true
    issuer: https://accounts.example.com        # discovery: <issuer>/.well-known/openid-configuration
    clientID: openctl
    clientSecretFile: oidc-client-secret         # relative → resolved under the state dir; never inline (matches tokenSecretFile)
    redirectURL: https://controller.lan:9445/auth/oidc/callback
    roleClaim: groups                            # which claim carries the role signal (default "groups")
    roleMapping:                                 # claim value → openctl role
      openctl-admins: admin
      openctl-editors: editor
      openctl-viewers: viewer
    defaultRole: ""                              # role for an authenticated user matching no mapping; "" = deny (recommended)
    usernameClaim: email                         # which claim becomes Principal.UserID (default "email")
```

Loaded alongside the existing auth config in `main.go`; `--no-auth` still
bypasses everything. Absent/`enabled:false` block = today's behavior exactly.

## Decisions (each needs a call; recommendation given)

### 1. Flow

- **(Recommended) Authorization Code + PKCE.** Browser-safe, no client secret
  in the browser, the current best practice for confidential *and* public
  clients. The controller is a confidential client (it has a secret) but PKCE
  costs nothing and hardens the callback.
- Alternative: implicit flow — deprecated, don't.

### 2. Claims → role mapping

- **(Recommended) Configurable `roleClaim` + explicit `roleMapping` table,
  deny-by-default.** A claim (default `groups`) carries values mapped to
  openctl roles; an authenticated user matching nothing gets `defaultRole`,
  which defaults to `""` = **deny** (fail closed). Group-based is the common
  IdP idiom (Google Workspace, Entra, Authentik, Keycloak all emit groups).
- Alternative: email→role table (simpler, but doesn't scale past a handful of
  users; can be layered as a second mapping source later).
- **Fail-closed matters:** never silently grant a role to any authenticated
  identity. An unmapped user is unauthorized, logged, and shown a clear "your
  account has no assigned role" page.

### 3. Where the client secret lives

- **(Recommended) `clientSecretFile`** (mode 0600, resolved under the state
  dir), never inline — matching the established `tokenSecretFile` convention
  and the repo's "no secrets in commits" rule.

### 4. CLI (non-browser) authentication

- **(Recommended) Out of scope for v1 OIDC; the CLI keeps using named tokens.**
  OIDC is browser-first. `users.yaml` named tokens remain the CLI/automation
  path and coexist unchanged. A **device-authorization-grant** flow for the
  CLI is a clean follow-on if wanted, but shouldn't gate the browser slice.

### 5. Provider discovery + verification library

- **(Recommended) OIDC discovery** (`<issuer>/.well-known/openid-configuration`)
  so only the issuer is configured, and **`github.com/coreos/go-oidc/v3` +
  `golang.org/x/oauth2`** for token exchange, JWKS fetch, and ID-token
  verification. These are the standard, audited Go libraries; hand-rolling JWT
  verification is the classic footgun. New deps, but narrow and well-vetted.

### 6. Coexistence + precedence

- OIDC, root token, and named users **all coexist**. Resolution order in
  `check` is unchanged (root → users → sessions); OIDC just produces sessions.
  A deployment can run OIDC for humans and named tokens for CI simultaneously.

### 7. Session lifetime + logout

- Reuse `DefaultSessionTTL` (7d) and the existing `Logout` (revokes the
  session row). **Open sub-question:** should logout also redirect to the IdP's
  end-session endpoint (RP-initiated logout)? Recommend a config toggle,
  default off (local session revocation is enough for the homelab threat
  model).

## Implementation sketch

- `internal/controller/auth/oidc.go`: `OIDCAuthenticator` wrapping `go-oidc`
  `Provider` + `oauth2.Config`; `AuthURL(state, verifier)` and
  `Exchange(ctx, code, verifier) (claims, error)`; `mapRole(claims) (Role,
  userID, error)` implementing decision 2 (fail-closed).
- `internal/controller/server/oidc_http.go`: the two `http.HandlerFunc`s
  (`/auth/oidc/login`, `/auth/oidc/callback`) mounted on the existing gateway
  mux; callback calls `SessionStore.Create` + sets the cookie via the same
  helper `Login` uses.
- `internal/config`: `AuthOIDCConfig` struct + loader (secret from file).
- `main.go`: construct the authenticator when `auth.oidc.enabled`, register the
  two routes. No change to the gRPC interceptor or RBAC.
- UI: a "Log in with SSO" button on the login screen that navigates to
  `/auth/oidc/login` (frontend follow-up; the backend slice is independently
  testable via curl).

## Testing plan (no real IdP required)

- **Fake IdP**: an `httptest` server exposing `/.well-known/openid-configuration`,
  a JWKS, and a token endpoint that issues **ID tokens signed with a test key**
  — the OIDC analog of `plugins/tf-fake`. Lets the full login→callback→session
  path run in a unit test with no external dependency.
- Cases: happy path (valid code → mapped role → session cookie set); invalid
  `state` rejected; expired / wrong-audience / bad-signature ID token rejected;
  unmapped user → deny (fail-closed) with no session minted; role mapping for
  each of viewer/editor/admin; coexistence (a named-token caller still works
  with OIDC enabled).
- Integration against a real IdP (Authentik/Keycloak in a container, or Google)
  is the manual validation gate before flipping `enabled` in a real
  deployment — the one step that genuinely needs an IdP.

## Non-goals

- CLI device-code flow (follow-on).
- SCIM / automated user provisioning.
- Fine-grained per-resource ACLs beyond the viewer/editor/admin ladder.
- Replacing named tokens — they remain the automation path.

## Rollout

Ship behind `auth.oidc.enabled` (default off). Unit-test against the fake IdP.
Validate against a real IdP in the homelab (this is the step that needs an
actual provider). Document the config in QUICKSTART/README. Only then is the
roadmap item's "OIDC" line done; named-token RBAC remains fully supported
alongside it.
