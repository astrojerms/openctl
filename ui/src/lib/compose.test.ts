import { afterEach, describe, expect, it, vi } from 'vitest';
import { templates, type Resource } from './api';
import { composeDefaults, composeDeployment, composeLoopback, guestTrustCommand } from './compose';
import { APP_API, VM_API } from './deployment';

const vm: Resource = { apiVersion: VM_API, kind: 'VirtualMachine', metadata: { name: 'docker' }, spec: { bootstrap: 'returned verbatim' } };
const app: Resource = {
  apiVersion: APP_API, kind: 'ComposeApplication', metadata: { name: 'welcome', annotations: { note: 'keep' } },
  spec: { target: { host: { $ref: { apiVersion: VM_API, kind: 'VirtualMachine', name: 'docker', field: 'status.ip' } }, privateKeyFile: '/keys/id', knownHostsFile: '/keys/trust' }, compose: 'returned verbatim', password: { $secret: { file: 'controller-only' } } },
};
const input = () => ({ ...composeDefaults(), vmName: 'docker', context: 'lab', node: 'pve1', templateVMID: 9000, sshKey: 'ssh-ed25519 public', privateKeyFile: '/keys/id', knownHostsFile: '/keys/trust' });
afterEach(() => vi.restoreAllMocks());

describe('controller-rendered Compose deployment', () => {
  it('renders two independent create-only resources with exact selected sizing and untouched manifests', async () => {
    const render = vi.spyOn(templates, 'render').mockResolvedValueOnce({ resource: vm }).mockResolvedValueOnce({ resource: app });
    const steps = await composeDeployment({ ...input(), cpuCores: 1, memoryMiB: 1024, diskGiB: 8 });
    expect(render.mock.calls[0]).toEqual(['docker-host-vm', { name: 'docker', context: 'lab', node: 'pve1', templateVMID: 9000, cpuCores: 1, memoryMiB: 1024, diskGiB: 8, storage: 'local-lvm', bridge: 'vmbr0', sshKey: 'ssh-ed25519 public' }, undefined]);
    expect(render.mock.calls[1]).toEqual(['compose-application', { name: 'welcome', vmName: 'docker', sshUser: 'ubuntu', privateKeyFile: '/keys/id', knownHostsFile: '/keys/trust', port: 18080, welcomeText: 'Welcome to OpenCtl' }, undefined]);
    expect(steps.map((step) => step.createOnly)).toEqual([true, true]);
    expect(steps[0].resource).toBe(vm); expect(steps[1].resource).toBe(app);
    expect(steps[1].resource.spec).toEqual(app.spec);
  });
  it('renders only the application for an existing guest, without implicit sizing, bootstrap, upgrades or publishing', async () => {
    const render = vi.spyOn(templates, 'render').mockResolvedValue({ resource: app });
    const steps = await composeDeployment({ ...input(), createVM: false, context: '', node: '', templateVMID: 0, cpuCores: NaN });
    expect(steps).toEqual([{ title: expect.any(String), resource: app, createOnly: true }]);
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toBe('compose-application');
    expect(Object.keys(render.mock.calls[0][1]).sort()).toEqual(['knownHostsFile', 'name', 'port', 'privateKeyFile', 'sshUser', 'vmName', 'welcomeText']);
  });
  it.each([{ context: '' }, { cpuCores: 0 }, { memoryMiB: 511 }, { diskGiB: 1.5 }, { port: 65536 }])('rejects incomplete intent before rendering: %j', async (patch) => {
    const render = vi.spyOn(templates, 'render');
    await expect(composeDeployment({ ...input(), ...patch })).rejects.toBeInstanceOf(Error);
    expect(render).not.toHaveBeenCalled();
  });
  it('blocks template failures and unexpected returned identities instead of constructing fallback manifests', async () => {
    const render = vi.spyOn(templates, 'render').mockRejectedValueOnce(new Error('bootstrap template unavailable'));
    await expect(composeDeployment(input())).rejects.toThrow('bootstrap template unavailable');
    render.mockResolvedValueOnce({ resource: app });
    await expect(composeDeployment(input())).rejects.toThrow(/different resource identity/);
  });
  it('shows an explicit exclusive controller-host action download, safely quoting the selected trust path', () => {
    const command = guestTrustCommand('docker', "~/private guest/known'hosts");
    expect(command).toContain('mkdir -p ~/.openctl/ssh');
    expect(command).toContain("VirtualMachine 'docker' ssh-host-keys --api-version proxmox.openctl.io/v1");
    expect(command).toContain('--download "$HOME"/');
    expect(command).toContain("known'\\''hosts");
    expect(command).not.toMatch(/keyscan|accept-new|StrictHostKeyChecking=no/);
  });
});

it('derives forwarding only from the reviewed guest-loopback probe, never an arbitrary public URL', () => {
  expect(composeLoopback({ ...app, spec: { probe: { url: 'http://127.0.0.1:19090/' } } })).toEqual({ url: 'http://127.0.0.1:19090/', port: '19090' });
  expect(composeLoopback({ ...app, spec: { probe: { url: 'https://public.example/' } } })).toBeNull();
  expect(composeLoopback(undefined)).toBeNull();
});
