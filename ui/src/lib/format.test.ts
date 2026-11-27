import { describe, expect, it } from 'vitest';
import { operationStatusBadge, statusBadge } from './format';

describe('statusBadge', () => {
  it('returns unknown for missing status', () => {
    expect(statusBadge(undefined)).toEqual({ label: '—', tone: 'unknown' });
  });

  it('maps Proxmox VM state.running to a "good" tone', () => {
    expect(statusBadge({ apiVersion: 'proxmox.openctl.io/v1', status: { state: 'running' } })).toEqual({ label: 'running', tone: 'good' });
  });

  it('maps k3s Cluster phase.ready to a "good" tone (lower-cased)', () => {
    // phase comes through from the controller capitalised; the label
    // preserves the original casing but tone is matched case-insensitively.
    expect(statusBadge({ apiVersion: 'k3s.openctl.io/v1', status: { phase: 'Ready' } })).toEqual({ label: 'Ready', tone: 'good' });
  });

  it('maps failed to bad', () => {
    expect(statusBadge({ apiVersion: 'k3s.openctl.io/v1', status: { phase: 'failed' } })).toEqual({ label: 'failed', tone: 'bad' });
  });

  it('falls back to "unknown" tone for unrecognised labels but preserves the label text', () => {
    expect(statusBadge({ apiVersion: 'example.io/v1', status: { state: 'frobnicating' } })).toEqual({
      label: 'frobnicating',
      tone: 'unknown',
    });
  });

  it('prefers state over phase when both are present', () => {
    expect(statusBadge({ apiVersion: 'proxmox.openctl.io/v1', status: { state: 'running', phase: 'failed' } })).toEqual({
      label: 'running',
      tone: 'good',
    });
  });

  it.each([
    [{ phase: 'ready' }, 'Unknown', 'unknown'],
    [{ phase: 'ready', ready: true, checks: { gpu: { state: 'not_ready' } } }, 'Not ready', 'warn'],
    [{ phase: 'ready', ready: true, checks: { gpu: { state: 'unknown' } } }, 'Unknown', 'unknown'],
    [{ phase: 'ready', ready: true, checks: [] }, 'Unknown', 'unknown'],
    [{ phase: 'ready', ready: false }, 'Not ready', 'warn'],
    [{ ready: true, checks: { http: { state: 'ready' }, publicEndpoint: { state: 'not_configured' } } }, 'Ready', 'good'],
  ] as const)('uses application readiness rather than lifecycle for %j', (status, label, tone) => {
    expect(statusBadge({ apiVersion: 'apps.openctl.io/v1', status })).toEqual({ label, tone });
  });
});

describe('operationStatusBadge', () => {
  it('maps active operation states to warn', () => {
    expect(operationStatusBadge('pending')).toEqual({ label: 'pending', tone: 'warn' });
    expect(operationStatusBadge('running')).toEqual({ label: 'running', tone: 'warn' });
  });

  it('maps succeeded operations to good', () => {
    expect(operationStatusBadge('succeeded')).toEqual({ label: 'succeeded', tone: 'good' });
  });

  it('maps failed terminal operation states to bad using backend spelling', () => {
    expect(operationStatusBadge('failed')).toEqual({ label: 'failed', tone: 'bad' });
    expect(operationStatusBadge('interrupted')).toEqual({ label: 'interrupted', tone: 'bad' });
    expect(operationStatusBadge('canceled')).toEqual({ label: 'canceled', tone: 'bad' });
  });

  it('keeps unknown operation states visible', () => {
    expect(operationStatusBadge('queued-ish')).toEqual({ label: 'queued-ish', tone: 'unknown' });
    expect(operationStatusBadge(undefined)).toEqual({ label: '—', tone: 'unknown' });
  });
});
