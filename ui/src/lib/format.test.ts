import { describe, expect, it } from 'vitest';
import { operationStatusBadge, statusBadge } from './format';

describe('statusBadge', () => {
  it('returns unknown for missing status', () => {
    expect(statusBadge(undefined)).toEqual({ label: '—', tone: 'unknown' });
  });

  it('maps Proxmox VM state.running to a "good" tone', () => {
    expect(statusBadge({ state: 'running' })).toEqual({ label: 'running', tone: 'good' });
  });

  it('maps k3s Cluster phase.ready to a "good" tone (lower-cased)', () => {
    // phase comes through from the controller capitalised; the label
    // preserves the original casing but tone is matched case-insensitively.
    expect(statusBadge({ phase: 'Ready' })).toEqual({ label: 'Ready', tone: 'good' });
  });

  it('maps failed to bad', () => {
    expect(statusBadge({ phase: 'failed' })).toEqual({ label: 'failed', tone: 'bad' });
  });

  it('falls back to "unknown" tone for unrecognised labels but preserves the label text', () => {
    expect(statusBadge({ state: 'frobnicating' })).toEqual({
      label: 'frobnicating',
      tone: 'unknown',
    });
  });

  it('prefers state over phase when both are present', () => {
    expect(statusBadge({ state: 'running', phase: 'failed' })).toEqual({
      label: 'running',
      tone: 'good',
    });
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
