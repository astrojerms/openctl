// Thin REST client over the controller's grpc-gateway surface (/v1/*).
//
// Auth model: the HTTP gateway turns a successful POST /v1/session/login
// response into an HttpOnly, SameSite=Strict session cookie. The browser
// auto-sends it on subsequent same-origin requests; this client never
// touches the raw token. `credentials: 'same-origin'` is the default on
// modern browsers but we set it explicitly so the contract is visible.
//
// 401 from any /v1 call is the controller's signal that the session is
// gone (expired, revoked, or never existed). Callers handle it by routing
// back to the login screen — the api layer doesn't redirect on its own
// because that couples transport to navigation.

export class ApiError extends Error {
  constructor(public status: number, public body: string) {
    super(`HTTP ${status}: ${body}`);
  }
}

export class UnauthorizedError extends ApiError {
  constructor(body: string) {
    super(401, body);
    this.name = 'UnauthorizedError';
  }
}

export interface RequestOptions {
  // Bearer token to send as Authorization header. Only used by the
  // initial Login call (the user pastes their root install-time token);
  // post-login calls rely on the HttpOnly session cookie set by the
  // gateway, so they leave bearer unset.
  bearer?: string;
  signal?: AbortSignal;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  opts?: RequestOptions,
): Promise<T> {
  const headers: Record<string, string> = {};
  let payload: BodyInit | undefined;
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  if (opts?.bearer) {
    headers['Authorization'] = `Bearer ${opts.bearer}`;
  }

  const resp = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: payload,
    signal: opts?.signal,
  });

  if (resp.status === 401) {
    throw new UnauthorizedError(await resp.text());
  }
  if (!resp.ok) {
    throw new ApiError(resp.status, await resp.text());
  }
  if (resp.status === 204) {
    return undefined as T;
  }
  const ct = resp.headers.get('Content-Type') ?? '';
  if (ct.startsWith('application/json')) {
    return (await resp.json()) as T;
  }
  return (await resp.text()) as unknown as T;
}

export const api = {
  get: <T>(path: string, opts?: RequestOptions) =>
    request<T>('GET', path, undefined, opts),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('POST', path, body ?? {}, opts),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>('PUT', path, body ?? {}, opts),
  delete: <T>(path: string, opts?: RequestOptions) =>
    request<T>('DELETE', path, undefined, opts),
};

// --- Typed wrappers for the endpoints U3.1 needs. -------------------------

export interface LoginResponse {
  token: string;
  sessionId: string;
  expiresAt: string;
}

export interface WhoAmIResponse {
  userId: string;
  sessionId: string;
  // RBAC role of the caller: "viewer" | "editor" | "admin". Empty when the
  // server didn't report one (older server, or --no-auth) — treated as
  // full access by the UI, since the server is the real gate.
  role?: string;
  // Canonical actor, including root/no-auth, independent of credential type.
  principalId?: string;
}

export const session = {
  // First-time login: the user supplies the install-time root bearer token,
  // server mints a session token, gateway middleware sets the HttpOnly
  // cookie. We never persist the root token in JS — once the cookie is
  // set, it's the browser's job.
  login: (rootToken: string, displayName: string, ttlSeconds = 0) =>
    api.post<LoginResponse>(
      '/v1/session/login',
      { displayName, ttlSeconds },
      { bearer: rootToken },
    ),
  logout: () => api.post<Record<string, never>>('/v1/session/logout'),
  whoami: (opts?: RequestOptions) => api.get<WhoAmIResponse>('/v1/session/whoami', opts),
};

export interface PingResponse {
  echo: string;
  serverVersion: string;
  // Short git SHA injected at build time. "dev" when unlinked.
  gitCommit?: string;
  // RFC3339 build timestamp. "dev" when unlinked.
  buildTime?: string;
  controllerId?: string;
  supportsIdempotentApply?: boolean;
  supportsIdempotentDelete?: boolean;
}

export const ping = {
  // PingService.Ping is GET /v1/ping?message=...; the message echo is
  // returned in the response body. No client-side payload needed beyond
  // the query string.
  ping: (message = 'hello from ui', opts?: RequestOptions) =>
    api.get<PingResponse>('/v1/ping?message=' + encodeURIComponent(message), opts),
};

// --- Schemas -------------------------------------------------------------

export interface SchemaInfo {
  apiVersion: string;
  kind: string;
  provider: string;
  fileName: string;
  // Composite-child hint, set by the controller when this kind is normally
  // produced by a parent composite (e.g. a k3s Cluster). ownerKind names that
  // parent; advancedNote explains it. Absent/false for ordinary top-level kinds.
  advanced?: boolean;
  ownerKind?: string;
  advancedNote?: string;
  // Discovery observations are not saved desired resources.
  observedOnly?: boolean;
}

export interface ListSchemasResponse {
  schemas?: SchemaInfo[];
}

export interface ValidateResponse {
  errors?: string[];
}

export interface GetFormSchemaResponse {
  json?: string;
}

export interface GetSchemaResponse {
  info?: SchemaInfo;
  // Raw CUE source text for the kind (multi-file packages joined).
  cueSource?: string;
}

export const schemas = {
  list: (opts?: RequestOptions) => api.get<ListSchemasResponse>('/v1/schemas', opts),
  validate: (resource: Resource | Partial<Resource>, opts?: RequestOptions) =>
    api.post<ValidateResponse>('/v1/schemas:validate', { resource }, opts),
  getForm: (apiVersion: string, kind: string, opts?: RequestOptions) =>
    api.post<GetFormSchemaResponse>('/v1/schemas:getForm', { apiVersion, kind }, opts),
  get: (apiVersion: string, kind: string, opts?: RequestOptions) =>
    api.post<GetSchemaResponse>('/v1/schemas:get', { apiVersion, kind }, opts),
};

// --- Resources -----------------------------------------------------------

export interface ResourceRef {
  apiVersion: string;
  kind: string;
  name: string;
}

export interface ResourceMetadata {
  name: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  // Arch Phase 8: populated for resources owned by another (e.g. a
  // VirtualMachine owned by its Cluster). Currently always 0 or 1 entry.
  ownerRefs?: ResourceRef[];
}

export interface DriftEntry {
  path: string;
  desired: string;
  observed: string;
}

export interface Resource {
  apiVersion: string;
  kind: string;
  metadata: ResourceMetadata;
  // spec/status are google.protobuf.Struct on the wire — JSON-shaped opaque
  // objects on this side. Components that drill into them do so behind
  // kind-aware adapters in src/lib/render.ts so the rendering surface stays
  // schema-aware even though the transport is generic.
  spec?: Record<string, unknown>;
  status?: Record<string, unknown>;
  drift?: DriftEntry[];
  // Arch Phase 8: child resources composed by this one (e.g. a Cluster's
  // member VMs). Empty for atomic resources.
  children?: ResourceRef[];
}

export interface ListResourcesResponse {
  resources?: Resource[];
}

// Saved desired intent is independent of provider observations. Metadata-only
// lists must never be promoted into Resource.status or mutation authority.
export interface SavedResourceMetadata {
  apiVersion: string;
  kind: string;
  metadata: ResourceMetadata;
  appliedAt?: string;
  source?: string;
}

export interface ListSavedResourcesResponse {
  resources?: SavedResourceMetadata[];
}

export interface GetSavedResourceResponse {
  resource: Resource;
  appliedAt?: string;
  source?: string;
}

export const inventoryApi = {
  list: (apiVersion: string, kind: string, opts?: RequestOptions) =>
    api.post<ListSavedResourcesResponse>('/v1/inventory:list', { apiVersion, kind }, opts),
  get: (apiVersion: string, kind: string, name: string, opts?: RequestOptions) =>
    api.post<GetSavedResourceResponse>('/v1/inventory:get', { apiVersion, kind, name }, opts),
};

export interface DryRunChildAction {
  verb: string;       // "create" | "destroy" | "respec" | "no-op"
  kind: string;
  name: string;
  detail?: string;
}

export interface DryRunApplyResponse {
  operation?: 'create' | 'update' | 'no-op';
  diff?: DriftEntry[];
  // Paths relative to metadata; desired = stored, observed = submitted.
  metadataDiff?: DriftEntry[];
  liveDrift?: DriftEntry[];
  children?: DryRunChildAction[];
  requiredGates?: string[];
  validationErrors?: string[];
  summary?: string;
  // Path-attributed schema violations for inline highlighting in the
  // form editor. Same set as validationErrors, but structured.
  fieldErrors?: FieldError[];
}

export interface FieldError {
  path?: string;
  message?: string;
}

// --- Composite DAG (Phase U9) --------------------------------------------

export interface GraphNode {
  id: string;         // "kind/name" — matches GraphEdge.from / .to
  apiVersion: string;
  kind: string;
  name: string;
  // "applied" | "pending" | "observed" | "missing"
  status: string;
  // false = observed-only / no applied manifest — render dim.
  managed?: boolean;
  // true for the composite resource the graph was requested for.
  root?: boolean;
}

export interface GraphEdge {
  from: string;       // GraphNode.id
  to: string;         // GraphNode.id
  relation: string;   // "owns" | "ref"
  field?: string;     // dotted ref source path, "ref" edges only
}

export interface ChildrenGraphResponse {
  nodes?: GraphNode[];
  edges?: GraphEdge[];
}

export interface GetResourceResponse {
  resource: Resource;
  // Desired manifest currently on file in applied_manifests; unset when
  // the resource was created out-of-band or has no manifest.
  applied?: Resource;
  // RFC3339 timestamp of the most recent apply; empty when `applied` is
  // unset.
  appliedAt?: string;
}

export interface ApplyRequest {
  resource: Resource | Partial<Resource>;
  allowDestructive?: boolean;
  iKnowThisBreaksTheCluster?: boolean;
  createOnly?: boolean;
  requestId?: string;
}

export interface ApplyResponse {
  operationId?: string;
  message?: string;
  requestId?: string;
}

export interface ActionParameterSpec {
  name: string;
  // "string" | "int" | "bool"
  type?: string;
  required?: boolean;
  description?: string;
  defaultValue?: string;
}

export interface ActionSpec {
  name: string;
  description?: string;
  parameters?: ActionParameterSpec[];
}

export interface ListActionsResponse {
  // Retained for backward-compat; actionSpecs is the richer form the UI uses.
  actions?: string[];
  actionSpecs?: ActionSpec[];
}

export interface InvokeActionResponse {
  message?: string;
  // External URL to open (e.g. Proxmox noVNC console). UI opens in a
  // new tab when set.
  url?: string;
  // Downloadable payload (e.g. k3s Cluster kubeconfig). UI serves as
  // a file download named downloadFilename with the given content.
  downloadContent?: string;
  downloadFilename?: string;
}

export interface DeleteResourceRequest {
  apiVersion: string;
  kind: string;
  name: string;
  requestId: string;
}

export interface DeleteResourceResponse {
  operationId?: string;
  message?: string;
  requestId?: string;
}

export interface TemplateSummary {
  name: string;
  displayName: string;
  description: string;
  apiVersion: string;
  kind: string;
}

export interface TemplateParameter {
  name: string;
  type: 'string' | 'int' | 'bool';
  description?: string;
  required?: boolean;
  // JSON-encoded default value; parse before use.
  defaultJson?: string;
  enum?: string[];
  optionsKind?: string;
}

export interface ListTemplatesResponse {
  templates?: TemplateSummary[];
}

export interface GetTemplateResponse {
  summary: TemplateSummary;
  parameters?: TemplateParameter[];
}

export interface RenderTemplateResponse {
  resource: Resource;
}

export interface ProviderEntry {
  name: string;
  endpoint?: string;
  tokenId?: string;
  hasSecret?: boolean;
  usesSecretFile?: boolean;
  caFile?: string;
  insecureSkipVerify?: boolean;
}

export interface ListProvidersResponse {
  providers?: ProviderEntry[];
}

export interface UpsertProviderRequest {
  name: string;
  endpoint: string;
  tokenId: string;
  tokenSecret?: string;
  caFile?: string;
  insecureSkipVerify?: boolean;
}

export const providersApi = {
  list: (opts?: RequestOptions) => api.get<ListProvidersResponse>('/v1/config/providers', opts),
  upsert: (req: UpsertProviderRequest, opts?: RequestOptions) =>
    api.post<{ provider: ProviderEntry }>('/v1/config/providers', req, opts),
  delete: (name: string, opts?: RequestOptions) =>
    api.delete<Record<string, never>>('/v1/config/providers/' + encodeURIComponent(name), opts),
};

export interface DiscoveryContext {
  name: string;
  endpoint?: string;
  defaultContext?: boolean;
}

export interface DiscoveredResource {
  context: string;
  node: string;
  vmid: string;
  identity: string;
  resource: Resource;
  managed?: boolean;
  adoptable?: boolean;
  reason?: string;
  identityConfirmationRequired?: boolean;
}

export interface MigrationBundle {
  version: number;
  resource: Resource;
  controllerId: string;
  manifestDigest: string;
  identity: string;
}

export interface TestDiscoveryConnectionResponse {
  connected?: boolean;
  message?: string;
  failureCode?: string;
}

export const discovery = {
  contexts: (apiVersion = 'proxmox.openctl.io/v1') =>
    api.get<{ contexts?: DiscoveryContext[] }>('/v1/discovery/contexts?api_version=' + encodeURIComponent(apiVersion)),
  inventory: (context: string, apiVersion = 'proxmox.openctl.io/v1') =>
    api.post<{ resources?: DiscoveredResource[] }>('/v1/discovery/resources', { apiVersion, context }),
  test: (context: string, apiVersion = 'proxmox.openctl.io/v1') =>
    api.post<TestDiscoveryConnectionResponse>('/v1/discovery:testConnection', { apiVersion, context }),
};

export const ownership = {
  confirmIdentity: (apiVersion: string, kind: string, name: string, identity: string, providerContext: string) =>
    api.post<{ resource: Resource; message?: string }>('/v1/resources:confirmIdentity', { apiVersion, kind, name, identity, providerContext }),
  adopt: (resource: Resource, providerContext: string) =>
    api.post<{ resource: Resource; message?: string }>('/v1/resources:adopt', { resource, providerContext }),
  adoptBundle: (bundle: MigrationBundle, providerContext: string) =>
    api.post<{ resource: Resource; message?: string }>('/v1/resources:adopt', { bundle, providerContext }),
  export: (apiVersion: string, kind: string, name: string, opts?: RequestOptions) =>
    api.post<{ bundle: MigrationBundle }>('/v1/resources:export', { apiVersion, kind, name }, opts),
  forget: (bundle: MigrationBundle, opts?: RequestOptions) =>
    api.post<{ message?: string }>('/v1/resources:forget', { bundle }, opts),
};

// Controller-behavior tunables (reconciler + operation retention). All are
// read once at controller startup, so restartRequired is always true.
export interface ControllerConfig {
  reconcilerEnabled?: boolean;
  reconcilerInterval?: string;   // Go duration string, e.g. "5m"
  opRetainPerResource?: number;
}

export interface ControllerConfigResponse {
  config?: ControllerConfig;
  restartRequired?: boolean;
  manifestMode?: 'mirror' | 'gitops';
}

export const controllerConfigApi = {
  get: () => api.get<ControllerConfigResponse>('/v1/config/controller'),
  update: (config: ControllerConfig) =>
    api.put<ControllerConfigResponse>('/v1/config/controller', { config }),
};

export const templates = {
  list: () => api.get<ListTemplatesResponse>('/v1/templates'),
  get: (name: string, opts?: RequestOptions) =>
    api.get<GetTemplateResponse>('/v1/templates/' + encodeURIComponent(name), opts),
  render: (name: string, params: Record<string, unknown>, opts?: RequestOptions) =>
    api.post<RenderTemplateResponse>(
      '/v1/templates/' + encodeURIComponent(name) + ':render',
      { name, params },
      opts,
    ),
};

export const resources = {
  list: (apiVersion: string, kind: string, opts?: RequestOptions) =>
    api.post<ListResourcesResponse>('/v1/resources:list', { apiVersion, kind }, opts),
  get: (apiVersion: string, kind: string, name: string, opts?: RequestOptions) =>
    api.post<GetResourceResponse>('/v1/resources:get', { apiVersion, kind, name }, opts),
  childrenGraph: (apiVersion: string, kind: string, name: string, opts?: RequestOptions) =>
    api.post<ChildrenGraphResponse>('/v1/resources:childrenGraph', { apiVersion, kind, name }, opts),
  dryRunApply: (resource: Resource | Partial<Resource>, opts?: RequestOptions) =>
    api.post<DryRunApplyResponse>('/v1/resources:dryRunApply', { resource }, opts),
  apply: (req: ApplyRequest, opts?: RequestOptions) =>
    api.post<ApplyResponse>('/v1/resources:apply', req, opts),
  listActions: (apiVersion: string, kind: string, opts?: RequestOptions) =>
    api.post<ListActionsResponse>('/v1/resources:listActions', { apiVersion, kind }, opts),
  invokeAction: (
    apiVersion: string,
    kind: string,
    resourceName: string,
    action: string,
    parameters?: Record<string, string>,
    opts?: RequestOptions,
  ) =>
    api.post<InvokeActionResponse>('/v1/resources:invokeAction', {
      apiVersion, kind, resourceName, action,
      ...(parameters && Object.keys(parameters).length > 0 ? { parameters } : {}),
    }, opts),
  delete: (req: DeleteResourceRequest, opts?: RequestOptions) =>
    api.post<DeleteResourceResponse>('/v1/resources:delete', req, opts),
};

// --- Operations ----------------------------------------------------------

export interface Operation {
  id: string;
  parentId?: string;
  type: string;
  apiVersion?: string;
  kind?: string;
  resourceName?: string;
  status: string;
  error?: string;
  submittedAt?: string;
  startedAt?: string;
  completedAt?: string;
  label?: string;
  source?: string;
  manifestJson?: string;
  requestId?: string;
  children?: Operation[];
}

export interface ListOperationsRequest {
  status?: string;
  apiVersion?: string;
  kind?: string;
  resourceName?: string;
  source?: string;
  since?: string;
  until?: string;
  limit?: number;
}

export interface ListOperationsResponse {
  operations?: Operation[];
}

export interface CancelOperationResponse {
  status?: string;
  message?: string;
}

export const operations = {
  get: (id: string, includeChildren = false, opts?: RequestOptions) =>
    api.get<Operation>(`/v1/operations/${encodeURIComponent(id)}${includeChildren ? '?include_children=true' : ''}`, opts),
  byRequest: (requestId: string, includeChildren = false, opts?: RequestOptions) =>
    api.post<Operation>('/v1/operations:byRequest', { requestId, includeChildren }, opts),
  list: (req: ListOperationsRequest = {}, opts?: RequestOptions) =>
    api.post<ListOperationsResponse>('/v1/operations:list', req, opts),
  cancel: (id: string) =>
    api.post<CancelOperationResponse>(`/v1/operations/${encodeURIComponent(id)}:cancel`, {}),
};

// --- Persisted control-plane overview (never probes providers) -------------

export interface ManagedKindCount {
  apiVersion: string;
  kind: string;
  resources: number;
}

export interface OverviewResponse {
  asOf: string;
  managedResources: number;
  kinds: ManagedKindCount[];
  pendingOperations: number;
  runningOperations: number;
  reviewResources: number;
  activeOperations: Operation[];
  reviewOperations: Operation[];
}

export const overviewApi = {
  get: (operationLimit = 10, opts?: RequestOptions) =>
    api.get<OverviewResponse>(`/v1/overview?operation_limit=${operationLimit}`, opts),
};

// --- Repo (manifest dir / git) -------------------------------------------

export interface RepoStatus {
  mode?: 'mirror' | 'gitops';
  enabled?: boolean;
  dir?: string;
  branch?: string;
  headSha?: string;
  remote?: string;
  clean?: boolean;
  dirtyPaths?: string[];
  // ahead/behind: -1 when there's no upstream branch (e.g. local-only repo).
  ahead?: number;
  behind?: number;
  pushMode?: 'onCommit' | 'periodic' | 'manual' | string;
}

export interface RepoActionResponse {
  message?: string;
}

export interface CommitInfo {
  sha?: string;
  author?: string;
  committedAt?: string;
  subject?: string;
}

export interface ResourceHistory {
  commits?: CommitInfo[];
}

export interface ResourceAtCommit {
  yaml?: string;
  existed?: boolean;
}

export interface ResourceRef {
  apiVersion: string;
  kind: string;
  name: string;
}

export const repo = {
  status: () => api.get<RepoStatus>('/v1/repo/status'),
  push: () => api.post<RepoActionResponse>('/v1/repo:push'),
  pull: () => api.post<RepoActionResponse>('/v1/repo:pull'),
  history: (r: ResourceRef, opts?: RequestOptions) =>
    api.post<ResourceHistory>('/v1/repo:history', r, opts),
  atCommit: (r: ResourceRef, sha: string, opts?: RequestOptions) =>
    api.post<ResourceAtCommit>('/v1/repo:atCommit', { ...r, sha }, opts),
};
