import { describe, expect, it } from 'vitest';
import { strFromU8, unzipSync } from 'fflate';
import { parseAllDocuments } from 'yaml';
import { deploymentArchive, deploymentManifest } from './deploymentExport';
import { jellyfinDeployment } from './jellyfin';
import type { DeploymentStep, JellyfinInputs } from './jellyfin';

const input: JellyfinInputs = {
  name: 'media', vmName: 'gpu', createVM: true, context: 'lab', node: 'pve1', templateVMID: 9000,
  storage: 'local-lvm', bridge: 'vmbr0', pciMapping: 'gpu', sshPublicKey: 'ssh-ed25519 fixture', ip: 'dhcp', gateway: '',
  sshUser: 'ubuntu', privateKeyFile: '/keys/guest', knownHostsFile: '/keys/known', image: 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
  nasServer: 'nas', nasShare: 'media', nasMediaPath: "Kid's Movies/日本語", secretProvider: 'file', nasSecret: 'nas',
  adminUser: 'admin', adminSecret: 'admin', viewerUser: 'viewer', viewerSecret: 'viewer',
  expose: true, hostname: 'media.example.com', tunnel: 'existing-tunnel', accountId: '', zoneId: '',
};
function manifest(bytes: Uint8Array) {
  const documents = parseAllDocuments(strFromU8(bytes));
  expect(documents).toHaveLength(1);
  expect(documents[0].errors).toEqual([]);
  return documents[0].toJS();
}

describe('staged deployment export', () => {
  it('keeps initial and final application versions separate, ordered and individually parseable', () => {
    const files = unzipSync(deploymentArchive(jellyfinDeployment(input)));
    expect(Object.keys(files)).toEqual(['01-virtualmachine.yaml', '02-jellyfin.yaml', '03-tunnelroute.yaml', '04-dnsrecord.yaml', '05-jellyfin.yaml']);
    const vm = manifest(files['01-virtualmachine.yaml']);
    const initial = manifest(files['02-jellyfin.yaml']);
    const route = manifest(files['03-tunnelroute.yaml']);
    const dns = manifest(files['04-dnsrecord.yaml']);
    const final = manifest(files['05-jellyfin.yaml']);
    expect(vm.metadata.labels).toEqual({ 'openctl.io/application': 'media' });
    expect(initial.metadata.name).toBe('media');
    expect(initial.spec.publicEndpoint).toBeUndefined();
    expect(final.metadata.name).toBe('media');
    expect(final.spec.publicEndpoint).toBe('https://media.example.com/System/Info/Public');
    expect(initial.spec.target.host).toEqual({ $ref: { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'gpu', field: 'status.ip' } });
    expect(initial.spec.nas.mediaPath).toBe("Kid's Movies/日本語");
    expect(initial.spec.nas.credentials).toEqual({ $secret: { provider: 'file', key: 'nas' } });
    expect(final.spec.admin.password).toEqual({ $secret: { provider: 'file', key: 'admin' } });
    expect(route.spec.service.$ref).toEqual({ apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', name: 'media', field: 'status.url' });
    expect(dns.spec.content.$ref.name).toBe('existing-tunnel');
  });

  it('exports desired metadata and secret markers without recovery identities, request consent or observed evidence', () => {
    const step = jellyfinDeployment({ ...input, createVM: false, expose: false })[0];
    step.resource.metadata.annotations = { note: 'Keep this note', 'openctl.io/allow-destructive': 'true', 'openctl.io/i-know-this-breaks-the-cluster': 'true' };
    step.resource.metadata.ownerRefs = [{ apiVersion: 'example.io/v1', kind: 'Owner', name: 'PRIVATE-OWNER' }];
    step.resource.status = { message: 'PRIVATE-LIVE-STATUS' };
    const recovered = { ...step, attempt: { requestId: 'PRIVATE-REQUEST', operationId: 'PRIVATE-OPERATION', createOnly: true, allowDestructive: true } };
    const files = unzipSync(deploymentArchive([recovered]));
    const text = strFromU8(files['01-jellyfin.yaml']);
    const resource = manifest(files['01-jellyfin.yaml']);
    expect(text).not.toContain('PRIVATE-');
    expect(resource.metadata.annotations).toEqual({ note: 'Keep this note' });
    expect(resource.metadata.ownerRefs).toBeUndefined();
    expect(resource.status).toBeUndefined();
    expect(resource.spec.viewer.password).toEqual({ $secret: { provider: 'file', key: 'viewer' } });
    expect(step.resource.metadata.annotations['openctl.io/allow-destructive']).toBe('true');
    expect(step.resource.status.message).toBe('PRIVATE-LIVE-STATUS');
  });

  it('keeps exported paths flat and current-stage numbering intact without using resource names as paths', () => {
    const step: DeploymentStep = { title: 'Untrusted name', createOnly: true, resource: { apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', metadata: { name: '../../.ssh/authorized_keys' }, spec: {} } };
    const file = deploymentManifest(step, 4);
    expect(file.filename).toBe('05-jellyfin.yaml');
    expect(parseAllDocuments(file.yaml)[0].toJS().metadata.name).toBe('../../.ssh/authorized_keys');
    const files = unzipSync(deploymentArchive([step]));
    expect(Object.keys(files)).toEqual(['01-jellyfin.yaml']);
  });
});
