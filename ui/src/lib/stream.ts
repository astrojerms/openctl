// Newline-delimited JSON streaming over fetch + ReadableStream. grpc-gateway
// wraps every server-streamed event as `{"result": <event>}\n`, so callers
// see a flat stream of typed events without thinking about the wrapper.
//
// The HTTP gateway holds the connection open indefinitely; cancellation
// happens via AbortSignal. On any transport error or non-2xx response, the
// returned promise rejects so callers can decide to retry — this module
// stays unopinionated about reconnect cadence.

import { UnauthorizedError, ApiError } from './api';

export interface StreamOptions {
  // AbortSignal — wire this to component unmount so server-side streams
  // tear down cleanly when the user navigates away.
  signal?: AbortSignal;
}

interface GatewayEnvelope<T> {
  // grpc-gateway's server-streaming wrapper. Either result is populated
  // (a successful event) or error is (the stream is terminating with
  // an RPC-level error mid-stream).
  result?: T;
  error?: { code?: number; message?: string };
}

export async function streamNdjson<T>(
  path: string,
  body: unknown,
  onEvent: (e: T) => void,
  opts?: StreamOptions,
): Promise<void> {
  const resp = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body ?? {}),
    signal: opts?.signal,
  });

  if (resp.status === 401) throw new UnauthorizedError(await resp.text());
  if (!resp.ok) throw new ApiError(resp.status, await resp.text());
  if (!resp.body) throw new Error('stream: response has no body');

  const reader = resp.body.getReader();
  const decoder = new TextDecoder('utf-8');
  let buf = '';
  // Read until the server closes the stream or the caller aborts.
  // AbortSignal aborts the underlying fetch which surfaces here as a
  // reader rejection — we let it propagate so the caller's catch block
  // sees the AbortError it asked for.
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });

    // Drain complete lines; partial trailing line stays in buf.
    let nl: number;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      let env: GatewayEnvelope<T>;
      try {
        env = JSON.parse(line);
      } catch (parseErr) {
        // grpc-gateway emits clean JSON per line; a parse error means
        // the stream is corrupted (proxy interference, etc.). Surface
        // and bail rather than silently swallow.
        throw new Error(`stream: failed to parse line: ${parseErr}`);
      }
      if (env.error) {
        throw new ApiError(500, env.error.message ?? 'stream error');
      }
      if (env.result !== undefined) onEvent(env.result);
    }
  }
}
