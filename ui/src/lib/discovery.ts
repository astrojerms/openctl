import type { MigrationBundle, Resource } from './api';

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// Shape validation is only a local usability check. The server verifies the
// digest, live VM identity, ownership, idle operations, and GitOps safeguards.
export function parseMigrationBundle(text: string): MigrationBundle {
  const value: unknown = JSON.parse(text);
  if (!object(value) || value.version !== 1 ||
      !['controllerId', 'manifestDigest', 'identity'].every((key) => typeof value[key] === 'string' && value[key] !== '')) {
    throw new Error('A complete version 1 migration bundle is required.');
  }
  const resource = value.resource;
  if (!object(resource) || resource.apiVersion !== 'proxmox.openctl.io/v1' || resource.kind !== 'VirtualMachine' ||
      !object(resource.metadata) || typeof resource.metadata.name !== 'string' || !resource.metadata.name || !object(resource.spec)) {
    throw new Error('The bundle must contain a complete standalone Proxmox VirtualMachine manifest.');
  }
  if (resource.status != null || (Array.isArray(resource.drift) && resource.drift.length > 0) ||
      (Array.isArray(resource.children) && resource.children.length > 0) ||
      (Array.isArray(resource.metadata.ownerRefs) && resource.metadata.ownerRefs.length > 0)) {
    throw new Error('Migration accepts desired intent only, without observed state, ownership, or children.');
  }
  return value as unknown as MigrationBundle;
}

export function observedDesired(resource: Resource): Resource {
  return {
    apiVersion: resource.apiVersion,
    kind: resource.kind,
    metadata: resource.metadata,
    spec: resource.spec,
  };
}
