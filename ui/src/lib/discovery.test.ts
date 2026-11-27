import { describe, expect, it } from 'vitest';
import type { MigrationBundle, Resource } from './api';
import { observedDesired, parseMigrationBundle } from './discovery';

const resource: Resource = {
  apiVersion: 'proxmox.openctl.io/v1',
  kind: 'VirtualMachine',
  metadata: { name: 'media', labels: { purpose: 'media' } },
  spec: { context: 'home', node: 'pve', cores: 4 },
};
const bundle: MigrationBundle = {
  version: 1, resource, controllerId: 'source-controller', manifestDigest: 'digest-verified-by-server', identity: 'live-identity',
};

function parse(value: unknown) { return parseMigrationBundle(JSON.stringify(value)); }

describe('parseMigrationBundle', () => {
  it('keeps the bundle unchanged, including source context and digest', () => {
    expect(parse(bundle)).toEqual(bundle);
  });

  it('surfaces invalid JSON and incomplete or unsupported bundles', () => {
    expect(() => parseMigrationBundle('{')).toThrow();
    for (const value of [null, [], {}, { ...bundle, version: 2 }, { ...bundle, identity: '' }, { ...bundle, manifestDigest: '' }, { ...bundle, controllerId: '' }]) {
      expect(() => parse(value)).toThrow('version 1 migration bundle');
    }
  });

  it('rejects other kinds and incomplete desired resources', () => {
    for (const invalid of [{ ...resource, kind: 'Cluster' }, { ...resource, apiVersion: 'other.io/v1' }, { ...resource, spec: [] }, { ...resource, metadata: { name: '' } }]) {
      expect(() => parse({ ...bundle, resource: invalid })).toThrow('standalone Proxmox VirtualMachine');
    }
  });

  it('rejects observed state, ownership, and composed children rather than stripping them', () => {
    for (const invalid of [
      { ...resource, status: {} },
      { ...resource, drift: [{ path: 'cores', desired: '4', observed: '2' }] },
      { ...resource, children: [{ apiVersion: 'apps.openctl.io/v1', kind: 'Application', name: 'jellyfin' }] },
      { ...resource, metadata: { ...resource.metadata, ownerRefs: [{ apiVersion: 'k3s.openctl.io/v1', kind: 'Cluster', name: 'cluster' }] } },
    ]) {
      expect(() => parse({ ...bundle, resource: invalid })).toThrow('desired intent only');
    }
  });
});

describe('observedDesired', () => {
  it('retains authoritative resource identity and spec but not observed state', () => {
    const observed = { ...resource, status: { state: 'running' }, drift: [{ path: 'cores', desired: '4', observed: '2' }] };
    expect(observedDesired(observed)).toEqual(resource);
    expect(observed.status.state).toBe('running');
  });
});
