import { describe, expect, it } from 'vitest';
import type { ResourceRef } from './api';
import { applicationCheckBadge, applicationChecks, applicationReadiness, childHealth, type ChildState } from './detailState';

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

describe('applicationReadiness', () => {
  it.each([
    [undefined, 'unknown'],
    [{ phase: 'Ready' }, 'unknown'],
    [{ ready: 'true' }, 'unknown'],
    [{ ready: true }, 'ready'],
    [{ ready: true, checks: {} }, 'ready'],
    [{ ready: true, checks: { gpu: { state: 'ready' }, publicEndpoint: { state: 'not_configured' } } }, 'ready'],
    [{ ready: false, checks: { http: { state: 'ready' } } }, 'not_ready'],
    [{ checks: { http: { state: 'ready' } } }, 'unknown'],
    [{ checks: { http: { state: 'not_ready' } } }, 'not_ready'],
  ] as const)('requires explicit aggregate readiness for %j', (status, expected) => {
    expect(applicationReadiness(status)).toBe(expected);
  });

  it.each([null, [], true, 'ready'])('does not let a malformed check container override a positive aggregate: %j', (checks) => {
    expect(applicationReadiness({ ready: true, checks })).toBe('unknown');
  });

  it.each([undefined, null, [], true, {}, { ready: true }, { state: 'future' }, { state: 'Ready' }, { state: 'unknown' }])('does not let malformed, skipped or obsolete evidence override a positive aggregate: %j', (gpu) => {
    expect(applicationReadiness({ ready: true, checks: { gpu } })).toBe('unknown');
  });

  it.each([
    { gpu: { state: 'unknown' }, http: { state: 'not_ready' } },
    { http: { state: 'not_ready' }, gpu: { state: 'unknown' } },
  ])('keeps observed failure decisive regardless of check ordering: %j', (checks) => {
    expect(applicationReadiness({ ready: true, checks })).toBe('not_ready');
    expect(applicationReadiness({ ready: false, checks })).toBe('not_ready');
  });
});
