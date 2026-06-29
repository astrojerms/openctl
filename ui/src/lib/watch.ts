// Typed wrappers around streamNdjson for the controller's two streaming
// RPCs. Each function takes a callback and an AbortSignal, returning the
// underlying fetch promise so the caller can attach error handling.
//
// On disconnect (server closes the stream, network blip, etc.) the
// promise resolves — there's no built-in reconnect. Components that want
// "always live" semantics wrap with a small retry loop; trying to bake
// retries in here would force one cadence on everyone.

import { streamNdjson, type StreamOptions } from './stream';
import type { Resource } from './api';

// --- Resource watch ------------------------------------------------------

export type WatchEventType = 'UNKNOWN' | 'ADDED' | 'MODIFIED' | 'DELETED';

export interface WatchEvent {
  type: WatchEventType;
  resource: Resource;
}

export function watchResources(
  apiVersion: string,
  kind: string,
  name: string | undefined,
  onEvent: (e: WatchEvent) => void,
  opts?: StreamOptions,
): Promise<void> {
  // grpc-gateway omits zero-value strings on the wire, so passing name=""
  // and name=undefined are equivalent. We pass undefined when not set so
  // wireshark traces don't confuse "watch all" with "watch the empty
  // name". Same logic for operations watch below.
  const body: Record<string, string> = { apiVersion, kind };
  if (name) body.name = name;
  return streamNdjson<WatchEvent>('/v1/resources:watch', body, onEvent, opts);
}

// --- Operations watch ----------------------------------------------------

export interface OperationRow {
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
  // UI Phase U7: caller source (cli / ui) and live substep children.
  // children populated only when WatchOperations is called with
  // includeChildren: true (default false to keep the firehose cheap).
  source?: string;
  children?: OperationRow[];
}

export interface OperationEvent {
  operation: OperationRow;
  terminal: boolean;
}

export interface OperationFilter {
  id?: string;
  apiVersion?: string;
  kind?: string;
  resourceName?: string;
  includeChildren?: boolean;
}

export function watchOperations(
  filter: OperationFilter,
  onEvent: (e: OperationEvent) => void,
  opts?: StreamOptions,
): Promise<void> {
  return streamNdjson<OperationEvent>('/v1/operations:watch', filter, onEvent, opts);
}
