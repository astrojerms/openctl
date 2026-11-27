import { ApiError, operations, resources, type GetResourceResponse, type Operation, type Resource, type ResourceRef } from './api';
import { resourceToYAML } from './yaml';
import { writable } from 'svelte/store';

export const RETRY_KEY = 'openctl.retryFromOp';
// A fresh click must reload an already-open editor even when its hash is unchanged.
// This is a notification only; storage and the fetched operation remain validated.
export const retryRevision = writable(0);

type RetryOperation = Operation & { apiVersion: string; kind: string; resourceName: string };
type RetryMarker = Pick<RetryOperation, 'id' | 'apiVersion' | 'kind' | 'resourceName' | 'type' | 'status'> & { version: 1 };
type RetryStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

export function canRetryOperation(op: Operation): op is RetryOperation {
  return op.type === 'apply' && ['failed', 'interrupted', 'canceled'].includes(op.status)
    && [op.id, op.apiVersion, op.kind, op.resourceName].every((value) => typeof value === 'string' && value.length > 0);
}

export function saveRetry(storage: RetryStorage, op: Operation): void {
  if (!canRetryOperation(op)) throw new Error('Only a failed, interrupted, or canceled Apply can be retried in the editor.');
  const marker: RetryMarker = {
    version: 1, id: op.id, apiVersion: op.apiVersion, kind: op.kind,
    resourceName: op.resourceName, type: op.type, status: op.status,
  };
  // A failed handoff must not navigate to an ordinary editor with stale intent.
  storage.setItem(RETRY_KEY, JSON.stringify(marker));
  retryRevision.update((revision) => revision + 1);
}

function readRetry(storage: RetryStorage, target: ResourceRef): RetryMarker | null {
  const raw = storage.getItem(RETRY_KEY);
  if (!raw) return null;
  let marker: RetryMarker;
  try { marker = JSON.parse(raw); } catch { return null; }
  // Legacy id-only and unrelated markers cannot authorize a missing baseline.
  if (!marker || marker.version !== 1 || !canRetryOperation(marker)
    || marker.apiVersion !== target.apiVersion || marker.kind !== target.kind || marker.resourceName !== target.name) return null;
  return marker;
}

// Remove inherited request authorization only when loading saved intent. Newly
// authored annotations remain editable; this helper never mutates its input.
export function withoutRequestConsent(resource: Resource): Resource {
  const annotations = resource.metadata.annotations;
  if (!annotations || (!Object.prototype.hasOwnProperty.call(annotations, 'openctl.io/allow-destructive')
    && !Object.prototype.hasOwnProperty.call(annotations, 'openctl.io/i-know-this-breaks-the-cluster'))) return resource;
  const desiredAnnotations = { ...annotations };
  delete desiredAnnotations['openctl.io/allow-destructive'];
  delete desiredAnnotations['openctl.io/i-know-this-breaks-the-cluster'];
  return { ...resource, metadata: { ...resource.metadata, annotations: desiredAnnotations } };
}

function retryManifest(op: Operation, marker: RetryMarker): Resource {
  if (!canRetryOperation(op) || op.id !== marker.id || op.apiVersion !== marker.apiVersion
    || op.kind !== marker.kind || op.resourceName !== marker.resourceName
    || op.type !== marker.type || op.status !== marker.status) {
    throw new Error('Retry operation no longer matches the selected failed Apply. Select Retry again from Operations.');
  }
  if (!op.manifestJson) throw new Error('Retry operation has no submitted manifest. Recover the original declaration before applying.');
  let manifest: Resource;
  try { manifest = JSON.parse(op.manifestJson); } catch { throw new Error('Retry operation contains an invalid submitted manifest.'); }
  if (!manifest || Array.isArray(manifest) || manifest.apiVersion !== marker.apiVersion || manifest.kind !== marker.kind
    || manifest.metadata?.name !== marker.resourceName || !manifest.spec || typeof manifest.spec !== 'object' || Array.isArray(manifest.spec)) {
    throw new Error('Retry manifest does not match the selected resource identity.');
  }
  return withoutRequestConsent(manifest);
}

export interface EditIntent {
  baseline: string;
  text: string;
  retryFromOpId: string;
  ownerRefs: ResourceRef[];
  children: ResourceRef[];
}

// Resolve explicit retry intent before looking for applied state: a first failed
// Apply has an operation but deliberately has no managed resource to Get.
export async function loadEditIntent(
  target: ResourceRef,
  storage: RetryStorage,
  getOperation = operations.get,
  getResource = resources.get,
): Promise<EditIntent> {
  const marker = readRetry(storage, target);
  const submitted = marker ? retryManifest(await getOperation(marker.id), marker) : null;
  let response: GetResourceResponse | null;
  try {
    response = await getResource(target.apiVersion, target.kind, target.name);
  } catch (err) {
    if (!submitted || !(err instanceof ApiError) || err.status !== 404) throw err;
    response = null;
  }
  const applied = response?.applied;
  const observed = response?.resource;
  const baseline = applied ? resourceToYAML(withoutRequestConsent(applied)) : observed ? resourceToYAML(withoutRequestConsent({
    apiVersion: target.apiVersion, kind: target.kind, metadata: observed.metadata, spec: observed.spec ?? {},
  })) : '';
  if (marker && JSON.stringify(readRetry(storage, target)) === JSON.stringify(marker)) storage.removeItem(RETRY_KEY);
  return {
    baseline, text: submitted ? resourceToYAML(submitted) : baseline,
    retryFromOpId: marker?.id ?? '',
    ownerRefs: observed?.metadata?.ownerRefs ?? applied?.metadata?.ownerRefs ?? submitted?.metadata?.ownerRefs ?? [],
    children: observed?.children ?? [],
  };
}
