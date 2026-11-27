import { describe, expect, it } from 'vitest';
import type { ResourceRef } from './api';
import { applicationCheckBadge, applicationChecks, childHealth, type ChildState } from './detailState';

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
  it('preserves evidence states with shared labels and only ready green', () => {
    const checks = applicationChecks({ checks: {
      application: { state: 'ready', message: 'Containers running' },
      http: { state: 'not_ready', message: 'Probe failed' },
      gpu: { state: 'unknown', message: 'Probe skipped' },
      publicEndpoint: { state: 'not_configured', message: 'No endpoint' },
    } });
    expect(checks.map((check) => check.state)).toEqual(['ready', 'not_ready', 'unknown', 'not_configured']);
    expect(checks.map((check) => applicationCheckBadge(check.state))).toEqual([
      { label: 'Ready', tone: 'good' }, { label: 'Not ready', tone: 'warn' },
      { label: 'Unknown', tone: 'unknown' }, { label: 'Not configured', tone: 'unknown' },
    ]);
    expect(checks.map((check) => check.message)).toEqual(['Containers running', 'Probe failed', 'Probe skipped', 'No endpoint']);
  });

  it('fails closed on missing, malformed, future, and obsolete evidence', () => {
    for (const value of [null, [], 'ready', {}, { ready: true }, { state: true }, { state: 'future' }, { state: 'Ready' }]) {
      expect(applicationChecks({ checks: { vm: value } })).toEqual([{ name: 'vm', state: 'unknown', message: '' }]);
    }
    expect(applicationChecks({ checks: { vm: { state: 'ready', message: {} } } })[0].message).toBe('');
    for (const checks of [undefined, null, [], true, 'ready']) expect(applicationChecks({ checks })).toEqual([]);
    expect(applicationChecks(undefined)).toEqual([]);
  });
});
