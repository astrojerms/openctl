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
  whoami: () => api.get<WhoAmIResponse>('/v1/session/whoami'),
};

export interface PingResponse {
  echo: string;
  serverVersion: string;
  // Short git SHA injected at build time. "dev" when unlinked.
  gitCommit?: string;
  // RFC3339 build timestamp. "dev" when unlinked.
  buildTime?: string;
}

export const ping = {
  // PingService.Ping is GET /v1/ping?message=...; the message echo is
  // returned in the response body. No client-side payload needed beyond
  // the query string.
  ping: (message = 'hello from ui') =>
    api.get<PingResponse>('/v1/ping?message=' + encodeURIComponent(message)),
};

// --- Schemas -------------------------------------------------------------

export interface SchemaInfo {
  apiVersion: string;
  kind: string;
  provider: string;
  fileName: string;
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

export const schemas = {
  list: () => api.get<ListSchemasResponse>('/v1/schemas'),
  validate: (resource: Resource | Partial<Resource>) =>
    api.post<ValidateResponse>('/v1/schemas:validate', { resource }),
  getForm: (apiVersion: string, kind: string) =>
    api.post<GetFormSchemaResponse>('/v1/schemas:getForm', { apiVersion, kind }),
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

export interface DryRunChildAction {
  verb: string;       // "create" | "destroy" | "respec" | "no-op"
  kind: string;
  name: string;
  detail?: string;
}

export interface DryRunApplyResponse {
  diff?: DriftEntry[];
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
}

export interface ApplyResponse {
  operationId?: string;
  message?: string;
}

export interface ListActionsResponse {
  actions?: string[];
}

export interface InvokeActionResponse {
  message?: string;
}

export interface DeleteResourceResponse {
  operationId?: string;
  message?: string;
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

export const templates = {
  list: () => api.get<ListTemplatesResponse>('/v1/templates'),
  get: (name: string) =>
    api.get<GetTemplateResponse>('/v1/templates/' + encodeURIComponent(name)),
  render: (name: string, params: Record<string, unknown>) =>
    api.post<RenderTemplateResponse>(
      '/v1/templates/' + encodeURIComponent(name) + ':render',
      { name, params },
    ),
};

export const resources = {
  list: (apiVersion: string, kind: string) =>
    api.post<ListResourcesResponse>('/v1/resources:list', { apiVersion, kind }),
  get: (apiVersion: string, kind: string, name: string) =>
    api.post<GetResourceResponse>('/v1/resources:get', { apiVersion, kind, name }),
  dryRunApply: (resource: Resource | Partial<Resource>) =>
    api.post<DryRunApplyResponse>('/v1/resources:dryRunApply', { resource }),
  apply: (req: ApplyRequest) =>
    api.post<ApplyResponse>('/v1/resources:apply', req),
  listActions: (apiVersion: string, kind: string) =>
    api.post<ListActionsResponse>('/v1/resources:listActions', { apiVersion, kind }),
  invokeAction: (apiVersion: string, kind: string, resourceName: string, action: string) =>
    api.post<InvokeActionResponse>('/v1/resources:invokeAction', {
      apiVersion, kind, resourceName, action,
    }),
  delete: (apiVersion: string, kind: string, name: string) =>
    api.post<DeleteResourceResponse>('/v1/resources:delete', {
      apiVersion, kind, name,
    }),
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
  get: (id: string, includeChildren = false) =>
    api.get<Operation>(`/v1/operations/${encodeURIComponent(id)}${includeChildren ? '?include_children=true' : ''}`),
  list: (req: ListOperationsRequest = {}) =>
    api.post<ListOperationsResponse>('/v1/operations:list', req),
  cancel: (id: string) =>
    api.post<CancelOperationResponse>(`/v1/operations/${encodeURIComponent(id)}:cancel`, {}),
};

// --- Repo (manifest dir / git) -------------------------------------------

export interface RepoStatus {
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

export const repo = {
  status: () => api.get<RepoStatus>('/v1/repo/status'),
  push: () => api.post<RepoActionResponse>('/v1/repo:push'),
  pull: () => api.post<RepoActionResponse>('/v1/repo:pull'),
};
