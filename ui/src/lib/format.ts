import type { Resource } from './api';
import { applicationCheckBadge, applicationReadiness } from './detailState';

// Applications require explicit, consistent readiness evidence. Other kinds
// retain provider lifecycle labels (Proxmox state, k3s phase), not app health.

export type StatusTone = 'good' | 'warn' | 'bad' | 'unknown';

export interface StatusBadge {
  label: string;
  tone: StatusTone;
}

const TONE_BY_LABEL: Record<string, StatusTone> = {
  // VM lifecycle (proxmox)
  running: 'good',
  stopped: 'warn',
  paused: 'warn',
  // Cluster phase (k3s)
  ready: 'good',
  applying: 'warn',
  'reboot-required': 'warn',
  degraded: 'bad',
  failed: 'bad',
  unknown: 'unknown',
};

export function statusBadge(resource: Pick<Resource, 'apiVersion' | 'status'> | undefined): StatusBadge {
  if (!resource) return { label: '—', tone: 'unknown' };
  if (resource.apiVersion === 'apps.openctl.io/v1') return applicationCheckBadge(applicationReadiness(resource.status));
  const status = resource.status;
  if (!status) return { label: '—', tone: 'unknown' };
  const raw = (status['state'] ?? status['phase'] ?? '') as unknown;
  if (typeof raw !== 'string' || raw === '') {
    return { label: '—', tone: 'unknown' };
  }
  const lower = raw.toLowerCase();
  return { label: raw, tone: TONE_BY_LABEL[lower] ?? 'unknown' };
}

export function operationStatusBadge(status: string | undefined): StatusBadge {
  switch (status) {
    case 'succeeded':
      return { label: status, tone: 'good' };
    case 'pending':
    case 'running':
      return { label: status, tone: 'warn' };
    case 'failed':
    case 'interrupted':
    case 'canceled':
      return { label: status, tone: 'bad' };
    default:
      return { label: status || '—', tone: 'unknown' };
  }
}
