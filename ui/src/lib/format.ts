// Tiny per-kind status/health adapter. Different providers stash the
// lifecycle string under different status keys (Proxmox VMs use
// `status.state`; k3s Clusters use `status.phase`), so we centralize the
// lookup. Returns a label + a tone the UI can colour-code; both default
// to "—" / "unknown" when the resource has no status yet.

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
  degraded: 'bad',
  failed: 'bad',
  unknown: 'unknown',
};

export function statusBadge(status: Record<string, unknown> | undefined): StatusBadge {
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
