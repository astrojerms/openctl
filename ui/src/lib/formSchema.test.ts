import { describe, expect, it } from 'vitest';
import { fromManifest, initialValue, scrubEmpty, type FormField } from './formSchema';

const cluster: FormField = {
  type: 'object',
  fields: [
    { name: 'apiVersion', type: 'string', const: 'k3s.openctl.io/v1' },
    { name: 'kind', type: 'string', const: 'Cluster' },
    {
      name: 'metadata',
      type: 'object',
      fields: [{ name: 'name', type: 'string' }],
    },
    {
      name: 'spec',
      type: 'object',
      fields: [
        {
          name: 'nodes',
          type: 'object',
          fields: [
            {
              name: 'controlPlane',
              type: 'object',
              fields: [{ name: 'count', type: 'int', min: 1, default: 1 }],
            },
            {
              name: 'workers',
              type: 'array',
              optional: true,
              items: {
                type: 'object',
                fields: [
                  { name: 'name', type: 'string' },
                  { name: 'count', type: 'int' },
                ],
              },
            },
          ],
        },
        {
          name: 'ssh',
          type: 'object',
          fields: [{ name: 'user', type: 'string', default: 'ubuntu' }],
        },
      ],
    },
  ],
};

describe('initialValue', () => {
  it('uses const over default', () => {
    expect(initialValue({ type: 'string', const: 'A', default: 'B' })).toBe('A');
  });
  it('uses default when no const', () => {
    expect(initialValue({ type: 'int', default: 7 })).toBe(7);
  });
  it('seeds objects with required children only', () => {
    const v = initialValue({
      type: 'object',
      fields: [
        { name: 'req', type: 'string' },
        { name: 'opt', type: 'string', optional: true },
      ],
    }) as Record<string, unknown>;
    expect(v).toEqual({ req: '' });
    expect('opt' in v).toBe(false);
  });
});

describe('fromManifest', () => {
  it('returns const for const fields regardless of data', () => {
    expect(fromManifest({ type: 'string', const: 'pinned' }, 'other')).toBe('pinned');
  });

  it('seeds a cluster from an existing manifest', () => {
    const data = {
      apiVersion: 'k3s.openctl.io/v1',
      kind: 'Cluster',
      metadata: { name: 'dev' },
      spec: {
        nodes: {
          controlPlane: { count: 3 },
          workers: [{ name: 'worker', count: 2 }],
        },
        ssh: { user: 'admin' },
      },
    };
    const v = fromManifest(cluster, data) as Record<string, unknown>;
    const spec = v.spec as Record<string, unknown>;
    const nodes = spec.nodes as Record<string, unknown>;
    expect((nodes.controlPlane as Record<string, unknown>).count).toBe(3);
    expect((nodes.workers as unknown[])).toHaveLength(1);
    expect((spec.ssh as Record<string, unknown>).user).toBe('admin');
  });

  it('fills required missing fields with initialValue', () => {
    const v = fromManifest(cluster, { apiVersion: 'k3s.openctl.io/v1' }) as Record<string, unknown>;
    expect((v.metadata as Record<string, unknown>).name).toBe('');
  });
});

describe('scrubEmpty', () => {
  it('drops empty optional fields from generated manifest', () => {
    const f: FormField = {
      type: 'object',
      fields: [
        { name: 'keep', type: 'string' },
        { name: 'opt', type: 'string', optional: true },
        { name: 'list', type: 'array', optional: true, items: { type: 'string' } },
      ],
    };
    const v = scrubEmpty(f, { keep: 'x', opt: '', list: [] }) as Record<string, unknown>;
    expect(v).toEqual({ keep: 'x' });
  });

  it('keeps empty required fields so server can flag them', () => {
    const f: FormField = {
      type: 'object',
      fields: [{ name: 'req', type: 'string' }],
    };
    const v = scrubEmpty(f, { req: '' }) as Record<string, unknown>;
    expect(v).toEqual({ req: '' });
  });
});
