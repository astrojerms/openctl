import type { Resource } from './api';

export const VM_API = 'proxmox.openctl.io/v1';
export const APP_API = 'apps.openctl.io/v1';

export interface JellyfinInputs {
  name: string;
  vmName: string;
  createVM: boolean;
  context: string;
  node: string;
  templateVMID: number;
  storage: string;
  bridge: string;
  pciMapping: string;
  sshPublicKey: string;
  ip: string;
  gateway: string;
  sshUser: string;
  privateKeyFile: string;
  knownHostsFile: string;
  image: string;
  nasServer: string;
  nasShare: string;
  nasMediaPath: string;
  secretProvider: string;
  nasSecret: string;
  adminUser: string;
  adminSecret: string;
  viewerUser: string;
  viewerSecret: string;
  expose: boolean;
  hostname: string;
  tunnel: string;
  accountId: string;
  zoneId: string;
}

export interface DeploymentStep {
  title: string;
  resource: Resource;
  createOnly: boolean;
}

function ref(apiVersion: string, kind: string, name: string, field: string) {
  return { $ref: { apiVersion, kind, name, field } };
}

export function jellyfinDeployment(input: JellyfinInputs): DeploymentStep[] {
  const name = input.name.trim();
  const vmName = input.vmName.trim();
  if (!/^[a-z][a-z0-9-]*$/.test(name) || !/^[a-z][a-z0-9-]*$/.test(vmName)) {
    throw new Error('Application and VM names must start with a letter and contain only lowercase letters, digits, and hyphens.');
  }
  if (!/^jellyfin\/jellyfin@sha256:[a-f0-9]{64}$/.test(input.image.trim())) {
    throw new Error('Use an official jellyfin/jellyfin image pinned to its published sha256 digest.');
  }
  const required = ['sshUser', 'privateKeyFile', 'knownHostsFile', 'nasServer', 'nasShare', 'secretProvider', 'nasSecret', 'adminUser', 'adminSecret', 'viewerUser', 'viewerSecret'] as const;
  for (const key of required) {
    if (!input[key].trim()) throw new Error(`${key} is required.`);
  }
  const secret = (key: string) => ({ $secret: { provider: input.secretProvider.trim(), key: key.trim() } });
  const steps: DeploymentStep[] = [];
  if (input.createVM) {
    if (!input.context || !input.node || !input.storage || !input.bridge || !input.pciMapping || !input.sshPublicKey.trim() || !input.ip.trim()) {
      throw new Error('Complete the VM placement, Ubuntu 24.04 template, GPU mapping, network, and SSH public key.');
    }
    if (!Number.isInteger(input.templateVMID) || input.templateVMID < 1) throw new Error('Choose a valid Ubuntu 24.04 template VMID.');
    if (input.ip !== 'dhcp' && !input.gateway.trim()) throw new Error('A static guest IP needs its reserved gateway.');
    steps.push({
      title: 'Provision dedicated GPU VM', createOnly: true,
      resource: {
        apiVersion: VM_API, kind: 'VirtualMachine', metadata: { name: vmName, labels: { 'openctl.io/application': name } },
        spec: {
          context: input.context, node: input.node, template: { vmid: input.templateVMID },
          cpu: { cores: 4, sockets: 1, type: 'host' }, memory: { size: 4096 },
          osType: 'l26', bios: 'ovmf', machine: 'q35', efiDisk: { storage: input.storage, type: '4m', preEnrolledKeys: false },
          hostPCI: [{ mapping: input.pciMapping, pcie: true }], agent: { enabled: true },
          disks: [{ name: 'scsi0', storage: input.storage, size: '32G', ssd: true, discard: true, iothread: true }],
          networks: [{ name: 'net0', bridge: input.bridge, model: 'virtio' }],
          cloudInit: {
            user: input.sshUser, sshKeys: [input.sshPublicKey.trim()], packages: ['qemu-guest-agent'],
            ipConfig: { net0: { ip: input.ip.trim(), ...(input.ip === 'dhcp' ? {} : { gateway: input.gateway.trim() }) } },
          }, startOnCreate: true,
        },
      },
    });
  }
  const app: Resource = {
    apiVersion: APP_API, kind: 'Jellyfin', metadata: { name },
    spec: {
      target: {
        host: ref(VM_API, 'VirtualMachine', vmName, 'status.ip'), user: input.sshUser.trim(),
        privateKeyFile: input.privateKeyFile.trim(), knownHostsFile: input.knownHostsFile.trim(), sudo: true,
      },
      allowRemoteAccess: input.expose,
      bootstrap: true, image: input.image.trim(),
      nas: { server: input.nasServer.trim(), share: input.nasShare.trim(), mediaPath: input.nasMediaPath.trim(), credentials: secret(input.nasSecret) },
      admin: { username: input.adminUser.trim(), password: secret(input.adminSecret) },
      viewer: { username: input.viewerUser.trim(), password: secret(input.viewerSecret) },
    },
  };
  steps.push({ title: 'Install and configure Jellyfin', resource: app, createOnly: true });
  if (input.expose) {
    const hostname = input.hostname.trim().toLowerCase();
    if (!/^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$/.test(hostname) || !input.tunnel.trim()) {
      throw new Error('Public exposure needs an existing connected Cloudflare tunnel and a valid hostname you control.');
    }
    steps.push({
      title: 'Route the public hostname', createOnly: true,
      resource: {
        apiVersion: 'cloudflare.openctl.io/v1', kind: 'TunnelRoute', metadata: { name: `${name}-route` },
        spec: {
          tunnel: input.tunnel.trim(), hostname, service: ref(APP_API, 'Jellyfin', name, 'status.url'),
          ...(input.accountId.trim() ? { accountId: input.accountId.trim() } : {}),
        },
      },
    }, {
      title: 'Publish DNS', createOnly: true,
      resource: {
        apiVersion: 'cloudflare.openctl.io/v1', kind: 'DNSRecord', metadata: { name: `${name}-dns` },
        spec: {
          type: 'CNAME', name: hostname, proxied: true,
          content: ref('cloudflare.openctl.io/v1', 'Tunnel', input.tunnel.trim(), 'status.cnameTarget'),
          ...(input.zoneId.trim() ? { zoneId: input.zoneId.trim() } : {}),
        },
      },
    }, {
      title: 'Verify public application health', createOnly: false,
      resource: { ...app, spec: { ...app.spec, publicEndpoint: `https://${hostname}/System/Info/Public` } },
    });
  }
  return steps;
}
