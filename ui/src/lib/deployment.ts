import type { Resource } from './api';

export const VM_API = 'proxmox.openctl.io/v1';
export const APP_API = 'apps.openctl.io/v1';
export type DeploymentProfile = 'jellyfin' | 'compose';
export const REVIEW_KEYS: Record<DeploymentProfile, string> = {
  jellyfin: 'openctl.jellyfinReview',
  compose: 'openctl.composeReview',
};
export interface DeploymentStep {
  title: string;
  resource: Resource;
  createOnly: boolean;
}
