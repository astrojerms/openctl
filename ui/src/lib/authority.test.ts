import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { controllerConfigApi, type ControllerConfigResponse } from './api';
import { auth, canMutate } from './auth';
import { canEditDesired, canManageOwnership, manifestAuthority, recordAuthority, refreshAuthority, resetAuthority } from './authority';

beforeEach(() => {
  auth.set({ kind: 'signed-in', me: { role: 'admin' } });
  resetAuthority();
});
afterEach(() => { vi.restoreAllMocks(); resetAuthority(); auth.set({ kind: 'unknown' }); });

describe('running manifest authority', () => {
  it('blocks desired-state changes in GitOps while preserving editor runtime permissions', () => {
    auth.set({ kind: 'signed-in', me: { role: 'editor' } });
    recordAuthority('gitops');
    expect(get(canEditDesired)).toBe(false);
    expect(get(canMutate)).toBe(true);
    expect(get(canManageOwnership)).toBe(false);
    recordAuthority('mirror');
    expect(get(canEditDesired)).toBe(true);
  });

  it('does not make a viewer writable when the controller owns intent', () => {
    auth.set({ kind: 'signed-in', me: { role: 'viewer' } });
    recordAuthority('mirror');
    expect(get(canEditDesired)).toBe(false);
    expect(get(canManageOwnership)).toBe(false);
  });

  it('fails closed after a previously writable controller stops reporting authority', async () => {
    recordAuthority('mirror');
    vi.spyOn(controllerConfigApi, 'get').mockRejectedValue(new Error('controller unavailable'));
    await refreshAuthority();
    expect(get(canEditDesired)).toBe(false);
    expect(get(manifestAuthority).error).toContain('controller unavailable');
  });

  it('does not let a stale login request re-enable writes after reset', async () => {
    const { promise, resolve } = Promise.withResolvers<ControllerConfigResponse>();
    vi.spyOn(controllerConfigApi, 'get').mockReturnValue(promise);
    const request = refreshAuthority();
    resetAuthority();
    recordAuthority('gitops');
    resolve({ manifestMode: 'mirror' });
    await request;
    expect(get(manifestAuthority).mode).toBe('gitops');
    expect(get(canEditDesired)).toBe(false);
  });
});
