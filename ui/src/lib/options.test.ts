import { describe, expect, it, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { ensureOptions, getOptions, optionsStore, refreshOptions } from './options';

// Mock the api module so ensureOptions doesn't try to hit a real
// endpoint. Each test sets its own resources.list implementation via
// vi.mocked before calling ensureOptions.
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api');
  return {
    ...actual,
    resources: {
      list: vi.fn(),
    },
  };
});

import { resources } from './api';

describe('options store', () => {
  beforeEach(() => {
    // Reset the store between tests by expiring every known key. The
    // store itself has no exported reset; refreshOptions clears the
    // (apiVersion, kind) pair we might have populated.
    for (const k of Object.keys(get(optionsStore).data)) {
      const [av, kind] = k.split('/').reduce<[string, string]>((acc, part, i, arr) => {
        // apiVersion contains a slash ("proxmox.openctl.io/v1"), so
        // the last segment is kind, everything else is apiVersion.
        if (i === arr.length - 1) return [acc[0], part];
        return [acc[0] ? `${acc[0]}/${part}` : part, acc[1]];
      }, ['', '']);
      refreshOptions(av, kind);
    }
    for (const k of Object.keys(get(optionsStore).errors)) {
      const [av, kind] = k.split('/').reduce<[string, string]>((acc, part, i, arr) => {
        if (i === arr.length - 1) return [acc[0], part];
        return [acc[0] ? `${acc[0]}/${part}` : part, acc[1]];
      }, ['', '']);
      refreshOptions(av, kind);
    }
    vi.mocked(resources.list).mockReset();
  });

  it('populates the cache with sorted names on success', async () => {
    vi.mocked(resources.list).mockResolvedValue({
      resources: [
        { metadata: { name: 'zebra' } },
        { metadata: { name: 'alpha' } },
        { metadata: { name: 'mango' } },
      ],
    } as unknown as Awaited<ReturnType<typeof resources.list>>);

    await ensureOptions('proxmox.openctl.io/v1', 'ProxmoxNode');
    expect(getOptions('proxmox.openctl.io/v1', 'ProxmoxNode')).toEqual(['alpha', 'mango', 'zebra']);
  });

  it('deduplicates concurrent requests for the same key', async () => {
    vi.mocked(resources.list).mockResolvedValue({
      resources: [{ metadata: { name: 'a' } }],
    } as unknown as Awaited<ReturnType<typeof resources.list>>);

    await Promise.all([
      ensureOptions('proxmox.openctl.io/v1', 'ProxmoxNode'),
      ensureOptions('proxmox.openctl.io/v1', 'ProxmoxNode'),
      ensureOptions('proxmox.openctl.io/v1', 'ProxmoxNode'),
    ]);
    // All three requests are deduped: only one fetch actually hits
    // the underlying API.
    expect(vi.mocked(resources.list)).toHaveBeenCalledTimes(1);
  });

  it('records error text on failure', async () => {
    vi.mocked(resources.list).mockRejectedValue(new Error('boom'));
    await ensureOptions('proxmox.openctl.io/v1', 'ProxmoxNode');
    const state = get(optionsStore);
    expect(state.errors['proxmox.openctl.io/v1/ProxmoxNode']).toBe('boom');
    expect(state.data['proxmox.openctl.io/v1/ProxmoxNode']).toBeUndefined();
  });
});
