import { describe, expect, it } from 'vitest';
import { extraKeys, fromManifest, initialValue, isEmpty, parseFreeform, scrubEmpty, type FormField } from './formSchema';

describe('isEmpty', () => {
  it('treats undefined and null as empty', () => {
    expect(isEmpty(undefined)).toBe(true);
    expect(isEmpty(null)).toBe(true);
  });
  it('treats empty strings as empty', () => {
    expect(isEmpty('')).toBe(true);
    expect(isEmpty('x')).toBe(false);
  });
  it('treats empty arrays as empty', () => {
    expect(isEmpty([])).toBe(true);
    expect(isEmpty([0])).toBe(false);
  });
  it('treats empty objects as empty', () => {
    expect(isEmpty({})).toBe(true);
    expect(isEmpty({ a: 1 })).toBe(false);
  });
  it('treats numbers and booleans as non-empty', () => {
    // These are valid scalars; collapse logic only looks at composite
    // emptiness, so scalars shouldn't read as "empty".
    expect(isEmpty(0)).toBe(false);
    expect(isEmpty(false)).toBe(false);
  });
});

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

describe('maps', () => {
  const labels: FormField = {
    type: 'object',
    fields: [{
      name: 'labels',
      type: 'map',
      optional: true,
      valueType: { type: 'string' },
    }],
  };

  it('fromManifest copies map entries through valueType', () => {
    const v = fromManifest(labels, { labels: { env: 'prod', tier: 'web' } }) as Record<string, unknown>;
    expect(v.labels).toEqual({ env: 'prod', tier: 'web' });
  });

  it('initialValue for map is an empty object', () => {
    expect(initialValue({ type: 'map', valueType: { type: 'string' } })).toEqual({});
  });

  it('preserves named empty values and invalid entries for validation', () => {
    const v = scrubEmpty(labels, {
      labels: { keep: 'x', empty: '', '': 'orphan' },
    }) as Record<string, unknown>;
    expect(v.labels).toEqual({ keep: 'x', empty: '', '': 'orphan' });
  });
});

describe('extraKeys', () => {
  const schema: FormField = {
    type: 'object',
    fields: [
      { name: 'apiVersion', type: 'string' },
      { name: 'kind', type: 'string' },
      {
        name: 'metadata',
        type: 'object',
        fields: [
          { name: 'name', type: 'string' },
          { name: 'labels', type: 'map', optional: true, valueType: { type: 'string' } },
        ],
      },
      {
        name: 'spec',
        type: 'object',
        fields: [
          { name: 'count', type: 'int' },
          {
            name: 'workers',
            type: 'array',
            optional: true,
            items: {
              type: 'object',
              fields: [{ name: 'name', type: 'string' }],
            },
          },
        ],
      },
    ],
  };

  it('returns no extras for a manifest that matches the schema', () => {
    const data = {
      apiVersion: 'k3s.openctl.io/v1',
      kind: 'Cluster',
      metadata: { name: 'dev', labels: { env: 'prod' } },
      spec: { count: 1, workers: [{ name: 'a' }] },
    };
    expect(extraKeys(schema, data)).toEqual([]);
  });

  it('reports unknown top-level keys', () => {
    const data = { spec: { count: 1 }, extras: 'huh' };
    expect(extraKeys(schema, data)).toContain('extras');
  });

  it('reports unknown nested keys with dotted path', () => {
    const data = { spec: { count: 1, mystery: 7 } };
    expect(extraKeys(schema, data)).toEqual(['spec.mystery']);
  });

  it('reports unknown keys inside array items', () => {
    const data = { spec: { count: 1, workers: [{ name: 'a', size: 'L' }] } };
    expect(extraKeys(schema, data)).toEqual(['spec.workers.0.size']);
  });

  it('does not flag map values as extras (keys are intrinsic to the type)', () => {
    const data = { metadata: { name: 'dev', labels: { whatever: 'x' } } };
    expect(extraKeys(schema, data)).toEqual([]);
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

describe('manifest form roundtrips', () => {
  const schema: FormField = {
    type: 'object',
    fields: [
      {
        name: 'metadata', type: 'object', fields: [
          { name: 'name', type: 'string' },
          { name: 'labels', type: 'map', optional: true, valueType: { type: 'string' } },
          { name: 'annotations', type: 'map', optional: true, valueType: { type: 'string' } },
        ],
      },
      {
        name: 'spec', type: 'object', fields: [
          { name: 'password', type: 'string', secret: true },
          { name: 'node', type: 'string' },
          { name: 'cpu', type: 'int' },
          { name: 'settings', type: 'object', fields: [{ name: 'enabled', type: 'bool' }] },
          { name: 'disks', type: 'array', items: { type: 'string' } },
          { name: 'environment', type: 'map', valueType: { type: 'string' } },
        ],
      },
    ],
  };

  it('keeps references opaque across scalar, object, array, and map schemas', () => {
    const manifest = {
      metadata: {
        name: 'media',
        labels: { category: '', environment: 'home' },
        annotations: { description: '', 'openctl.io/example': 'retained' },
      },
      spec: {
        password: { $secret: { provider: 'vault', key: 'media/password' } },
        node: { $ref: 'ProxmoxNode/host.status.name' },
        cpu: { $ref: { kind: 'VirtualMachine', name: 'source', path: 'spec.cpu' } },
        settings: { $ref: 'Config/default.spec.settings' },
        disks: [{ $ref: 'Volume/media.status.path' }],
        environment: { TOKEN: { $secret: { env: 'MEDIA_TOKEN' } } },
      },
    };
    expect(extraKeys(schema, manifest)).toEqual([]);
    const form = fromManifest(schema, manifest) as typeof manifest;
    form.metadata.name = 'media-renamed';
    expect(scrubEmpty(schema, form)).toEqual({
      ...manifest,
      metadata: { ...manifest.metadata, name: 'media-renamed' },
    });
  });

  it.each([
    [{ type: 'string' }, 42],
    [{ type: 'int' }, 'invalid'],
    [{ type: 'bool' }, 'false'],
    [{ type: 'array', items: { type: 'string' } }, { unexpected: true }],
    [{ type: 'map', valueType: { type: 'string' } }, ['invalid']],
    [{ type: 'object', fields: [{ name: 'enabled', type: 'bool' }] }, 'invalid'],
  ] as Array<[FormField, unknown]>)('keeps invalid input for server validation (%j)', (field, data) => {
    expect(scrubEmpty(field, fromManifest(field, data))).toEqual(data);
  });

  it('preserves freeform object fields without a child schema', () => {
    const field: FormField = { type: 'object' };
    const data = { arbitrary: { nested: [1, true, 'value'] } };
    expect(extraKeys(field, data)).toEqual([]);
    expect(scrubEmpty(field, fromManifest(field, data))).toEqual(data);
  });
});

describe('freeform form input', () => {
  it.each([
    ['{"mounts":["/media"],"enabled":true}', { mounts: ['/media'], enabled: true }],
    ['mounts:\n  - /media\nenabled: true\n', { mounts: ['/media'], enabled: true }],
    ['false', false],
    ['42', 42],
    ['"false"', 'false'],
    ['null', null],
    ['', undefined],
    ['$secret:\n  env: MEDIA_TOKEN', { $secret: { env: 'MEDIA_TOKEN' } }],
  ])('parses JSON/YAML instead of storing textarea text (%s)', (text, value) => {
    expect(parseFreeform(text as string)).toEqual({ value, error: '' });
  });

  it('returns syntax errors instead of dropping the input or emitting a string', () => {
    const result = parseFreeform('mounts: [unterminated');
    expect(result.error).not.toBe('');
    expect(result).not.toHaveProperty('value');
  });
});
