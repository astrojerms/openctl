import { describe, expect, it } from 'vitest';
import { jellyfinDeployment } from './jellyfin';
import type { JellyfinInputs } from './jellyfin';

const input: JellyfinInputs = {
  name: 'media', vmName: 'gpu', createVM: true, context: 'lab', node: 'pve1', templateVMID: 9000,
  cpuCores: 4, memoryMiB: 4096, diskGiB: 32, storage: 'local-lvm', bridge: 'vmbr0', pciMapping: 'gpu',
  sshPublicKey: 'ssh-ed25519 fixture', ip: 'dhcp', gateway: '', sshUser: 'ubuntu',
  privateKeyFile: '/keys/guest', knownHostsFile: '/keys/known', image: 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
  nasServer: 'nas', nasShare: 'media', nasMediaPath: '', secretProvider: 'file', nasSecret: 'nas',
  adminUser: 'admin', adminSecret: 'admin', viewerUser: 'viewer', viewerSecret: 'viewer',
  expose: false, hostname: '', tunnel: '', accountId: '', zoneId: '',
};

describe('guided dedicated VM sizing', () => {
  it('places chosen capacity on only the new VM and preserves GPU/bootstrap/reference configuration', () => {
    const [vm, app] = jellyfinDeployment({ ...input, cpuCores: 6, memoryMiB: 12288, diskGiB: 80 });
    expect(vm.createOnly).toBe(true);
    expect(vm.resource.spec?.cpu).toEqual({ cores: 6, sockets: 1, type: 'host' });
    expect(vm.resource.spec?.memory).toEqual({ size: 12288 });
    expect(vm.resource.spec?.disks).toEqual([{ name: 'scsi0', storage: 'local-lvm', size: '80G', ssd: true, discard: true, iothread: true }]);
    expect(vm.resource.spec?.hostPCI).toEqual([{ mapping: 'gpu', pcie: true }]);
    expect(vm.resource.spec?.bios).toBe('ovmf');
    expect(vm.resource.spec?.machine).toBe('q35');
    expect(app.resource.spec?.target).toMatchObject({ host: { $ref: { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'gpu', field: 'status.ip' } } });
    expect(app.resource.spec).not.toHaveProperty('cpu');
    expect(app.resource.spec?.bootstrap).toBe(true);
  });

  it('accepts the native schema minimums without inventing a different capacity', () => {
    const [vm] = jellyfinDeployment({ ...input, cpuCores: 1, memoryMiB: 512, diskGiB: 1 });
    expect(vm.resource.spec?.cpu).toMatchObject({ cores: 1 });
    expect(vm.resource.spec?.memory).toEqual({ size: 512 });
    expect((vm.resource.spec?.disks as { size: string }[])[0].size).toBe('1G');
  });

  it.each([0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, undefined])('rejects invalid CPU sizing before preparing resources: %s', (cpuCores) => {
    expect(() => jellyfinDeployment({ ...input, cpuCores: cpuCores! })).toThrow(/CPU cores/);
  });
  it.each([0, 511, -1, 1024.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, undefined])('rejects invalid MiB sizing before preparing resources: %s', (memoryMiB) => {
    expect(() => jellyfinDeployment({ ...input, memoryMiB: memoryMiB! })).toThrow(/VM memory/);
  });
  it.each([0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, undefined])('rejects invalid GiB sizing before preparing resources: %s', (diskGiB) => {
    expect(() => jellyfinDeployment({ ...input, diskGiB: diskGiB! })).toThrow(/VM OS disk/);
  });

  it('does not create or resize an existing guest, including when hidden provisioning sizes are invalid', () => {
    const steps = jellyfinDeployment({ ...input, createVM: false, cpuCores: 0, memoryMiB: NaN, diskGiB: -1 });
    expect(steps.map((step) => step.resource.kind)).toEqual(['Jellyfin']);
    expect(steps[0].resource.spec?.target).toMatchObject({ host: { $ref: { name: 'gpu' } } });
    expect(steps[0].resource.spec).not.toHaveProperty('cpu');
    expect(steps[0].resource.spec).not.toHaveProperty('memory');
    expect(steps[0].resource.spec).not.toHaveProperty('disks');
  });
});
