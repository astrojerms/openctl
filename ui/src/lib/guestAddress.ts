import type { Resource } from './api';

const guestAddressFailures: Record<string, true> = {
  vm_not_running: true, vm_nic_unavailable: true, guest_agent_unavailable: true,
  guest_agent_invalid_response: true, guest_ip_unavailable: true, guest_ip_ambiguous: true,
};

export function guestAddressObservation(status: Resource['status']) {
  const diagnostic = status?.ipObservation;
  const unknown = { state: 'unknown', code: '', message: '', ip: '' };
  if (!diagnostic || typeof diagnostic !== 'object' || Array.isArray(diagnostic)) return unknown;
  const { state, code, message } = diagnostic as Record<string, unknown>;
  const safeCode = typeof code === 'string' && /^[a-z][a-z0-9_]{0,79}$/.test(code) ? code : '';
  if (!safeCode || typeof message !== 'string' || !message.trim()) return unknown;
  if (state === 'available' && safeCode === 'guest_ip_available'
    && status?.state === 'running' && typeof status.ip === 'string' && status.ip.trim()) {
    return { state, code: safeCode, message, ip: status.ip.trim() };
  }
  if (state === 'unavailable' && Object.hasOwn(guestAddressFailures, safeCode)) {
    return { state, code: safeCode, message, ip: '' };
  }
  return { ...unknown, code: safeCode };
}
