import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { streamNdjson } from './stream';
import { ApiError, UnauthorizedError } from './api';

// Helper: build a Response whose body is a ReadableStream that emits the
// supplied chunks one at a time. Each chunk lands as a separate read, so
// the tests can exercise the line-splitter's handling of partial lines.
function ndjsonResponse(chunks: string[], status = 200): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(encoder.encode(c));
      controller.close();
    },
  });
  return new Response(body, { status });
}

let originalFetch: typeof fetch;

beforeEach(() => {
  originalFetch = globalThis.fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

describe('streamNdjson', () => {
  it('parses each {"result": ...} line and invokes onEvent in order', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      ndjsonResponse([
        '{"result":{"n":1}}\n',
        '{"result":{"n":2}}\n',
        '{"result":{"n":3}}\n',
      ]),
    ) as unknown as typeof fetch;

    const seen: Array<{ n: number }> = [];
    await streamNdjson<{ n: number }>('/test', {}, (e) => seen.push(e));
    expect(seen).toEqual([{ n: 1 }, { n: 2 }, { n: 3 }]);
  });

  it('reassembles lines split across chunks', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      // Split a single line ("{...,\"n\":42}}") in the middle of a JSON
      // number, then a second complete line in a third chunk.
      ndjsonResponse([
        '{"result":{"n":4',
        '2}}\n{"result":{"n":7}}',
        '\n',
      ]),
    ) as unknown as typeof fetch;

    const seen: Array<{ n: number }> = [];
    await streamNdjson<{ n: number }>('/test', {}, (e) => seen.push(e));
    expect(seen).toEqual([{ n: 42 }, { n: 7 }]);
  });

  it('throws UnauthorizedError on 401 before reading the body', async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response('go away', { status: 401 })) as unknown as typeof fetch;
    await expect(streamNdjson('/test', {}, () => {})).rejects.toBeInstanceOf(
      UnauthorizedError,
    );
  });

  it('throws ApiError on non-2xx', async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response('boom', { status: 503 })) as unknown as typeof fetch;
    await expect(streamNdjson('/test', {}, () => {})).rejects.toBeInstanceOf(ApiError);
  });

  it('throws ApiError when the gateway emits an {"error":...} envelope mid-stream', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      ndjsonResponse([
        '{"result":{"n":1}}\n',
        '{"error":{"code":13,"message":"upstream down"}}\n',
      ]),
    ) as unknown as typeof fetch;

    const seen: Array<{ n: number }> = [];
    await expect(
      streamNdjson<{ n: number }>('/test', {}, (e) => seen.push(e)),
    ).rejects.toMatchObject({ message: expect.stringContaining('upstream down') });
    // The pre-error event still arrived.
    expect(seen).toEqual([{ n: 1 }]);
  });

  it('passes through blank lines silently', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      ndjsonResponse(['\n\n{"result":{"n":1}}\n\n']),
    ) as unknown as typeof fetch;

    const seen: Array<{ n: number }> = [];
    await streamNdjson<{ n: number }>('/test', {}, (e) => seen.push(e));
    expect(seen).toEqual([{ n: 1 }]);
  });
});
