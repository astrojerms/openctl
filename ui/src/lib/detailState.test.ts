import { describe, expect, it } from 'vitest';
import type { ResourceRef } from './api';
import { applicationChecks, childHealth, type ChildState } from './detailState';

const refs: ResourceRef[] = [
  { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'one' },
  { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'two' },
];
const first = 'proxmox.openctl.io/v1/VirtualMachine/one';
const second = 'proxmox.openctl.io/v1/VirtualMachine/two';
const healthy: ChildState = { badge: { label: 'running', tone: 'good' }, driftCount: 0 };

describe('childHealth', () => {
  it('never marks missing, loading, or unknown children green', () => {
    for (const state of [undefined, null, { badge: { label: '—', tone: 'unknown' as const }, driftCount: 0 }]) {
      const states: Record<string, ChildState | null> = { [first]: healthy };
      if (state !== undefined) states[second] = state;
      expect(childHealth(refs, states)).toEqual({ drifted: 0, unhealthy: 0, unknown: 1, tone: 'unknown' });
    }
  });

  it('is healthy only after every referenced child is known healthy', () => {
    expect(childHealth(refs, { [first]: healthy, [second]: healthy })).toEqual({ drifted: 0, unhealthy: 0, unknown: 0, tone: 'good' });
    expect(childHealth([], {})).toEqual({ drifted: 0, unhealthy: 0, unknown: 0, tone: 'unknown' });
  });

  it('counts read errors, nonhealthy children, and drift independently', () => {
    expect(childHealth(refs, {
      [first]: { badge: { label: '—', tone: 'unknown' }, driftCount: 0, error: 'read failed' },
      [second]: { badge: { label: 'stopped', tone: 'warn' }, driftCount: 2 },
    })).toEqual({ drifted: 1, unhealthy: 2, unknown: 0, tone: 'warn' });
  });
});

describe('applicationChecks', () => {
  it('keeps explicit failed and unknown readiness separate from successful checks', () => {
    expect(applicationChecks({ checks: {
      application: { ready: true, message: 'Containers running' },
      http: { ready: false, message: 'Probe failed' },
      gpu: { message: 'Not observed' },
      publicEndpoint: { ready: false, message: 'Not configured; no endpoint was published' },
    } })).toEqual([
      { name: 'application', ready: true, message: 'Containers running' },
      { name: 'http', ready: false, message: 'Probe failed' },
      { name: 'gpu', ready: null, message: 'Not observed' },
      { name: 'publicEndpoint', ready: false, message: 'Not configured; no endpoint was published' },
    ]);
  });

  it('does not fabricate checks when status or checks are unavailable', () => {
    expect(applicationChecks(undefined)).toEqual([]);
    expect(applicationChecks({ checks: [] })).toEqual([]);
    expect(applicationChecks({ checks: { vm: null } })).toEqual([{ name: 'vm', ready: null, message: '' }]);
  });
});
