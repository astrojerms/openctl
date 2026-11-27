import { describe, expect, it, vi } from 'vitest';
import { ApiError, UnauthorizedError, type GetResourceResponse, type Operation, type Resource } from './api';
import { canRetryOperation, loadEditIntent, RETRY_KEY, retryRevision, saveRetry, withoutRequestConsent } from './retry';
import { parseYAML } from './yaml';
import { get } from 'svelte/store';

const target = { apiVersion: 'apps.openctl.io/v1', kind: 'ComposeApplication', name: 'media' };
const manifest: Resource = {
  apiVersion: target.apiVersion, kind: target.kind, metadata: { name: target.name, labels: { purpose: 'media' } },
  spec: { target: { host: 'bad-host' }, compose: { $secret: { file: 'compose' } }, environment: { $secret: { env: 'APP_ENV' } } },
};
const operation: Operation = {
  id: 'failed-initial-apply', type: 'apply', status: 'failed', apiVersion: target.apiVersion,
  kind: target.kind, resourceName: target.name, manifestJson: JSON.stringify(manifest),
};
function storage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value); },
    removeItem: (key: string) => { values.delete(key); },
  };
}
const existing: GetResourceResponse = {
  resource: { ...manifest, spec: { observed: true } }, applied: { ...manifest, spec: { lastSuccessful: true } },
};

describe('failed Apply retry handoff', () => {
  it('loads exact failed intent before Get, including secret markers, when the first Apply has no managed baseline', async () => {
    const handoff = storage();
    saveRetry(handoff, operation);
    const calls: string[] = [];
    const getOp = vi.fn(async () => { calls.push('operation'); return operation; });
    const getResource = vi.fn(async () => { calls.push('resource'); throw new ApiError(404, 'not managed'); });
    const intent = await loadEditIntent(target, handoff, getOp, getResource);
    expect(calls).toEqual(['operation', 'resource']);
    expect(intent.baseline).toBe('');
    expect(parseYAML(intent.text).doc).toEqual(manifest);
    expect(intent.retryFromOpId).toBe(operation.id);
    expect(handoff.getItem(RETRY_KEY)).toBeNull();
    expect(getOp).toHaveBeenCalledExactlyOnceWith(operation.id);
  });

  it('uses last-successful intent only as a baseline, never as the failed draft', async () => {
    const handoff = storage();
    saveRetry(handoff, operation);
    const intent = await loadEditIntent(target, handoff, async () => operation, async () => existing);
    expect(parseYAML(intent.text).doc).toEqual(manifest);
    expect(parseYAML(intent.baseline).doc).toEqual(existing.applied);
  });

  it('blocks explicit retries when the operation cannot be loaded instead of falling back to applied intent', async () => {
    const handoff = storage();
    saveRetry(handoff, operation);
    const getResource = vi.fn(async () => existing);
    await expect(loadEditIntent(target, handoff, async () => { throw new ApiError(404, 'expired operation'); }, getResource)).rejects.toThrow('expired operation');
    expect(getResource).not.toHaveBeenCalled();
    expect(handoff.getItem(RETRY_KEY)).not.toBeNull();
  });

  it.each([new ApiError(503, 'controller down'), new UnauthorizedError('expired session'), new Error('network unavailable')])(
    'does not mistake a baseline outage for a first Apply: %s', async (failure) => {
      const handoff = storage();
      saveRetry(handoff, operation);
      await expect(loadEditIntent(target, handoff, async () => operation, async () => { throw failure; })).rejects.toBe(failure);
      expect(handoff.getItem(RETRY_KEY)).not.toBeNull();
    },
  );

  it.each([
    { ...operation, apiVersion: 'other.io/v1' }, { ...operation, kind: 'Jellyfin' },
    { ...operation, resourceName: 'other' }, { ...operation, id: 'other-op' },
    { ...operation, type: 'delete' }, { ...operation, status: 'succeeded' }, { ...operation, status: 'canceled' },
    { ...operation, manifestJson: '' }, { ...operation, manifestJson: '{' },
    { ...operation, manifestJson: JSON.stringify({ ...manifest, apiVersion: 'other.io/v1' }) },
    { ...operation, manifestJson: JSON.stringify({ ...manifest, kind: 'Jellyfin' }) },
    { ...operation, manifestJson: JSON.stringify({ ...manifest, metadata: { name: 'other' } }) },
  ])('rejects changed or mismatched operation/manifest identity without fetching a baseline', async (invalid) => {
    const handoff = storage();
    saveRetry(handoff, operation);
    const getResource = vi.fn(async () => existing);
    await expect(loadEditIntent(target, handoff, async () => invalid, getResource)).rejects.toThrow();
    expect(getResource).not.toHaveBeenCalled();
  });

  it.each([operation.id, JSON.stringify(operation), JSON.stringify({
    version: 1, ...operation, apiVersion: 'unrelated.io/v1',
  })])('does not trust old or unrelated session markers: %s', async (marker) => {
    const handoff = storage();
    handoff.setItem(RETRY_KEY, marker);
    const getOp = vi.fn(async () => operation);
    await expect(loadEditIntent(target, handoff, getOp, async () => { throw new ApiError(404, 'not managed'); })).rejects.toThrow('not managed');
    expect(getOp).not.toHaveBeenCalled();
  });

  it('retains observed and submitted ownership guards', async () => {
    const ownerRefs = [{ apiVersion: 'k3s.openctl.io/v1', kind: 'Cluster', name: 'owner' }];
    for (const response of [null, { ...existing, resource: { ...existing.resource, metadata: { ...manifest.metadata, ownerRefs } } }]) {
      const handoff = storage();
      const owned = { ...operation, manifestJson: JSON.stringify({ ...manifest, metadata: { ...manifest.metadata, ownerRefs } }) };
      saveRetry(handoff, owned);
      const intent = await loadEditIntent(target, handoff, async () => owned, async () => {
        if (!response) throw new ApiError(404, 'not managed');
        return response;
      });
      expect(intent.ownerRefs).toEqual(ownerRefs);
    }
  });

  it('requires fresh destructive consent while retaining desired metadata and secret intent', async () => {
    const handoff = storage();
    const intent = { ...manifest, metadata: { ...manifest.metadata, annotations: {
      'openctl.io/allow-destructive': 'true', 'openctl.io/i-know-this-breaks-the-cluster': 'true', purpose: 'keep',
    } } };
    const failed = { ...operation, manifestJson: JSON.stringify(intent) };
    saveRetry(handoff, failed);
    const loaded = await loadEditIntent(target, handoff, async () => failed, async () => { throw new ApiError(404, 'not managed'); });
    expect(parseYAML(loaded.text).doc).toEqual({ ...manifest, metadata: { ...manifest.metadata, annotations: { purpose: 'keep' } } });
    expect(JSON.parse(failed.manifestJson).metadata.annotations['openctl.io/allow-destructive']).toBe('true');
  });

  it('notifies an already-open editor on each explicit retry, including the same operation', () => {
    const handoff = storage();
    const before = get(retryRevision);
    saveRetry(handoff, operation);
    saveRetry(handoff, operation);
    expect(get(retryRevision)).toBe(before + 2);
  });

  it('offers retries only for failed Apply and surfaces handoff persistence failures', () => {
    for (const status of ['failed', 'interrupted', 'canceled']) expect(canRetryOperation({ ...operation, status })).toBe(true);
    for (const invalid of [{ ...operation, type: 'delete' }, { ...operation, type: 'action' }, { ...operation, status: 'running' }]) {
      expect(canRetryOperation(invalid)).toBe(false);
      expect(() => saveRetry(storage(), invalid)).toThrow();
    }
    expect(() => saveRetry({ ...storage(), setItem: () => { throw new Error('Storage unavailable'); } }, operation)).toThrow('Storage unavailable');
  });
});

describe('inherited request consent', () => {
  const ownerRefs = [{ apiVersion: 'k3s.openctl.io/v1', kind: 'Cluster', name: 'owner' }];
  const saved: Resource = { ...manifest, metadata: { ...manifest.metadata, ownerRefs, annotations: {
    'openctl.io/allow-destructive': 'true', 'openctl.io/i-know-this-breaks-the-cluster': 'true', purpose: 'keep',
  } } };
  const expected = { ...manifest, metadata: { ...manifest.metadata, ownerRefs, annotations: { purpose: 'keep' } } };

  it('removes only reserved consent without mutating saved intent or copying secret specs', () => {
    const desired = withoutRequestConsent(saved);
    expect(desired).toEqual(expected);
    expect(desired.spec).toBe(saved.spec);
    expect(saved.metadata.annotations?.['openctl.io/allow-destructive']).toBe('true');
    expect(saved.metadata.annotations?.['openctl.io/i-know-this-breaks-the-cluster']).toBe('true');
    expect(withoutRequestConsent(manifest)).toBe(manifest);
  });

  it.each([true, false])('strips inherited consent from ordinary Edit applied/observed baseline (applied=%s)', async (hasApplied) => {
    const loaded = await loadEditIntent(target, storage(), async () => operation, async () => ({
      resource: saved, ...(hasApplied ? { applied: saved } : {}),
    }));
    expect(parseYAML(loaded.text).doc).toEqual(expected);
    expect(loaded.baseline).toBe(loaded.text);
    expect(loaded.ownerRefs).toEqual(ownerRefs);
    expect(saved.metadata.annotations?.['openctl.io/allow-destructive']).toBe('true');
  });
});
