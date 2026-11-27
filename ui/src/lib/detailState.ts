import type { ResourceRef } from './api';
import type { StatusBadge, StatusTone } from './format';

export interface ChildState { badge: StatusBadge; driftCount: number; error?: string }

export function childHealth(refs: ResourceRef[], states: Record<string, ChildState | null>): { drifted: number; unhealthy: number; unknown: number; tone: StatusTone } {
  let drifted = 0;
  let unhealthy = 0;
  let unknown = 0;
  for (const ref of refs) {
    const state = states[`${ref.apiVersion}/${ref.kind}/${ref.name}`];
    if (state?.driftCount) drifted++;
    if (state?.error || state?.badge.tone === 'bad' || state?.badge.tone === 'warn') unhealthy++;
    else if (!state || state.badge.tone !== 'good') unknown++;
  }
  return { drifted, unhealthy, unknown, tone: drifted || unhealthy ? 'warn' : unknown || !refs.length ? 'unknown' : 'good' };
}

export interface AppCheck { name: string; ready: boolean | null; message: string }

export function applicationChecks(status: Record<string, unknown> | undefined): AppCheck[] {
  const checks = status?.checks;
  if (!checks || typeof checks !== 'object' || Array.isArray(checks)) return [];
  return Object.entries(checks).map(([name, value]) => {
    const check = value && typeof value === 'object' ? value as Record<string, unknown> : {};
    return { name, ready: typeof check.ready === 'boolean' ? check.ready : null, message: typeof check.message === 'string' ? check.message : '' };
  });
}
