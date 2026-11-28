import { templates, type RequestOptions, type Resource } from './api';
import { APP_API, VM_API, type DeploymentStep } from './deployment';

export interface ComposeInputs {
  name: string;
  vmName: string;
  createVM: boolean;
  context: string;
  node: string;
  templateVMID: number;
  cpuCores: number;
  memoryMiB: number;
  diskGiB: number;
  storage: string;
  bridge: string;
  sshKey: string;
  sshUser: string;
  privateKeyFile: string;
  knownHostsFile: string;
  port: number;
  welcomeText: string;
}
export function composeDefaults(): ComposeInputs {
  return {
    name: 'welcome', vmName: '', createVM: true, context: '', node: '', templateVMID: 0,
    cpuCores: 2, memoryMiB: 2048, diskGiB: 16, storage: 'local-lvm', bridge: 'vmbr0',
    sshKey: '', sshUser: 'ubuntu', privateKeyFile: '', knownHostsFile: '~/.openctl/ssh/welcome_known_hosts',
    port: 18080, welcomeText: 'Welcome to OpenCtl',
  };
}
export async function composeDeployment(input: ComposeInputs, opts?: RequestOptions): Promise<DeploymentStep[]> {
  if (!/^[a-z][a-z0-9-]*$/.test(input.name) || !/^[a-z][a-z0-9-]*$/.test(input.vmName)) throw new Error('Choose application and VM names using lowercase letters, numbers and hyphens.');
  if (![input.sshUser, input.privateKeyFile, input.knownHostsFile].every((value) => value.trim())) throw new Error('Supply the SSH user and controller-host private key and verified known_hosts file paths, never key contents.');
  if (!Number.isSafeInteger(input.port) || input.port < 1 || input.port > 65535) throw new Error('Choose a whole-number loopback port between 1 and 65535.');
  if (input.createVM) {
    if (![input.context, input.node, input.storage, input.bridge, input.sshKey].every((value) => value.trim())) throw new Error('Explicitly select the Proxmox context, node, storage, bridge and guest public key.');
    for (const [name, value, minimum] of [['Template VMID', input.templateVMID, 1], ['CPU cores', input.cpuCores, 1], ['VM memory (MiB)', input.memoryMiB, 512], ['OS disk (GiB)', input.diskGiB, 8]] as const) {
      if (!Number.isSafeInteger(value) || value < minimum) throw new Error(`${name} must be a whole number of at least ${minimum}.`);
    }
  }
  const steps: DeploymentStep[] = [];
  if (input.createVM) {
    const { resource } = await templates.render('docker-host-vm', {
      name: input.vmName, context: input.context, node: input.node, templateVMID: input.templateVMID,
      cpuCores: input.cpuCores, memoryMiB: input.memoryMiB, diskGiB: input.diskGiB,
      storage: input.storage, bridge: input.bridge, sshKey: input.sshKey,
    }, opts);
    if (resource.apiVersion !== VM_API || resource.kind !== 'VirtualMachine' || resource.metadata.name !== input.vmName) throw new Error('The Docker host template returned a different resource identity. No Apply was submitted.');
    steps.push({ title: 'Provision Ubuntu Docker guest', resource, createOnly: true });
  }
  const { resource } = await templates.render('compose-application', {
    name: input.name, vmName: input.vmName, sshUser: input.sshUser, privateKeyFile: input.privateKeyFile,
    knownHostsFile: input.knownHostsFile, port: input.port, welcomeText: input.welcomeText,
  }, opts);
  if (resource.apiVersion !== APP_API || resource.kind !== 'ComposeApplication' || resource.metadata.name !== input.name) throw new Error('The application template returned a different resource identity. No Apply was submitted.');
  steps.push({ title: 'Deploy Docker welcome application', resource, createOnly: true });
  return steps;
}

export function shellArgument(value: string): string { return `'${value.replaceAll("'", "'\\''")}'`; }
export function shellFile(value: string): string {
  return value.startsWith('~/') ? `"$HOME"/${shellArgument(value.slice(2))}` : shellArgument(value);
}
export function guestTrustCommand(vmName: string, knownHostsFile: string): string {
  return `mkdir -p ~/.openctl/ssh\nopenctl ctl action run VirtualMachine ${shellArgument(vmName)} ssh-host-keys --api-version ${VM_API} --download ${shellFile(knownHostsFile)}`;
}

export function composeLoopback(resource: Resource | undefined): { url: string; port: string } | null {
  const probe = resource?.spec?.probe as { url?: unknown } | undefined;
  if (typeof probe?.url !== 'string') return null;
  try {
    const url = new URL(probe.url);
    if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || url.username || url.password) return null;
    return { url: url.href, port: url.port || '80' };
  } catch { return null; }
}
