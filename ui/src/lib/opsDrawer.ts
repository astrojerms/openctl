import { writable } from 'svelte/store';

export interface OpsDrawerFocus {
  apiVersion?: string;
  kind?: string;
  resourceName?: string;
  opId?: string;
}

export const opsDrawerFocus = writable<OpsDrawerFocus | null>(null);

export function focusOpsDrawer(focus: OpsDrawerFocus): void {
  opsDrawerFocus.set(focus);
}

export function clearOpsDrawerFocus(): void {
  opsDrawerFocus.set(null);
}
