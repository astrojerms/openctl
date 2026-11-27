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

export type AppCheckState = 'ready' | 'not_ready' | 'unknown' | 'not_configured';
export interface AppCheck { name: string; state: AppCheckState; message: string }

const CHECK_BADGES: Record<AppCheckState, StatusBadge> = {
  ready: { label: 'Ready', tone: 'good' },
  not_ready: { label: 'Not ready', tone: 'warn' },
  unknown: { label: 'Unknown', tone: 'unknown' },
  not_configured: { label: 'Not configured', tone: 'unknown' },
};

export function applicationCheckBadge(state: AppCheckState): StatusBadge {
  return CHECK_BADGES[state];
}

export function applicationChecks(status: Record<string, unknown> | undefined): AppCheck[] {
  const checks = status?.checks;
  if (!checks || typeof checks !== 'object' || Array.isArray(checks)) return [];
  return Object.entries(checks).map(([name, value]) => {
    const check = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
    const state = check.state === 'ready' || check.state === 'not_ready' || check.state === 'not_configured' ? check.state : 'unknown';
    return { name, state, message: typeof check.message === 'string' ? check.message : '' };
  });
}
