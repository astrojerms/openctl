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
}

export const ping = {
  // PingService.Ping is GET /v1/ping?message=...; the message echo is
  // returned in the response body. No client-side payload needed beyond
  // the query string.
  ping: (message = 'hello from ui') =>
    api.get<PingResponse>('/v1/ping?message=' + encodeURIComponent(message)),
};
