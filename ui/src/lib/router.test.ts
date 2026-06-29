import { describe, expect, it } from 'vitest';
import { routeHref } from './router';

describe('routeHref', () => {
  it('encodes home as the root hash', () => {
    expect(routeHref({ name: 'home' })).toBe('#/');
  });

  it('splits apiVersion into group/version segments for human-readable URLs', () => {
    expect(
      routeHref({ name: 'list', apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine' }),
    ).toBe('#/k/proxmox.openctl.io/v1/VirtualMachine');
  });

  it('appends an encoded resource name on detail routes', () => {
    expect(
      routeHref({
        name: 'detail',
        apiVersion: 'k3s.openctl.io/v1',
        kind: 'Cluster',
        resourceName: 'dev cluster',
      }),
    ).toBe('#/k/k3s.openctl.io/v1/Cluster/dev%20cluster');
  });
});
