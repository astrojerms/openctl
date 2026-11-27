import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { fromStore, writable } from 'svelte/store';
import TemplateWizard from './TemplateWizard.svelte';
import { ApiError, operations, ping, resources, session, templates, type ApplyRequest, type ApplyResponse, type DryRunApplyResponse, type GetTemplateResponse, type Operation, type PingResponse, type RenderTemplateResponse, type Resource } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';

const templateName = 'machine-template';
const me = { role: 'admin', userId: 'display-name', principalId: 'canonical-actor', sessionId: 'session-one' };
const controller: PingResponse = { echo: '', serverVersion: 'dev', controllerId: 'controller-one', supportsIdempotentApply: true };
const definition: GetTemplateResponse = {
  summary: { name: templateName, displayName: 'Machine template', description: 'Create a machine', apiVersion: 'test.openctl.io/v1', kind: 'Machine' },
  parameters: [
    { name: 'name', type: 'string', defaultJson: '"default-machine"' },
    { name: 'password', type: 'string' },
  ],
};
const selection = writable(templateName);
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;
let entries: Map<string, string>;

function renderResource(params: Record<string, unknown>): Resource {
  return {
    apiVersion: definition.summary.apiVersion,
    kind: definition.summary.kind,
    metadata: { name: String(params.name), labels: { team: 'reviewed' }, annotations: { owner: 'operator' } },
    spec: { password: params.password ?? { $secret: { file: 'controller-password' } }, image: { $ref: { kind: 'Image', name: 'approved' } } },
  };
}
function operation(requestId: string, status = 'failed', name = 'chosen-machine'): Operation {
  return { id: 'apply-operation', type: 'apply', status, requestId, apiVersion: definition.summary.apiVersion, kind: definition.summary.kind, resourceName: name, error: status === 'failed' ? 'provider failed' : undefined };
}

beforeEach(() => {
  vi.useFakeTimers();
  entries = new Map();
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => { entries.set(key, value); },
    removeItem: (key: string) => { entries.delete(key); },
    clear: () => entries.clear(),
    key: (index: number) => [...entries.keys()][index] ?? null,
    get length() { return entries.size; },
  } satisfies Storage);
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected HTTP in isolated template test')));
  target = document.createElement('div');
  document.body.append(target);
  selection.set(templateName);
  auth.set({ kind: 'signed-in', me });
  recordAuthority('mirror');
  vi.spyOn(ping, 'ping').mockResolvedValue(controller);
  vi.spyOn(session, 'whoami').mockResolvedValue(me);
  vi.spyOn(templates, 'get').mockResolvedValue(definition);
  vi.spyOn(templates, 'render').mockImplementation(async (_name, params) => ({ resource: renderResource(params) }));
  vi.spyOn(resources, 'dryRunApply').mockResolvedValue({ operation: 'create', summary: 'Fresh create preview' });
  vi.spyOn(resources, 'apply').mockRejectedValue(new Error('acknowledgement dropped'));
  vi.spyOn(operations, 'byRequest').mockRejectedValue(new Error('Unexpected recovery'));
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  resetAuthority();
  auth.set({ kind: 'unknown' });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
async function settle() {
  for (let i = 0; i < 12; i++) { await Promise.resolve(); await tick(); }
}
async function preview() {
  await settle();
  await vi.advanceTimersByTimeAsync(350);
  await settle();
}
async function open() {
  const current = fromStore(selection);
  component = mount(TemplateWizard, { target, props: { get template() { return current.current; } } });
  await preview();
}
function button(label: string): HTMLButtonElement {
  const node = [...target.querySelectorAll<HTMLButtonElement>('button')].find((entry) => entry.textContent?.trim() === label);
  if (!node) throw new Error(`Missing button: ${label}`);
  return node;
}
function input(name: string): HTMLInputElement {
  const node = target.querySelector<HTMLInputElement>(`#param-${name}`);
  if (!node) throw new Error(`Missing parameter: ${name}`);
  return node;
}
function setParam(name: string, value: string) {
  input(name).value = value;
  input(name).dispatchEvent(new Event('input', { bubbles: true }));
}
async function createChosen() {
  await open();
  setParam('name', 'chosen-machine');
  setParam('password', 'literal-secret-do-not-store');
  await preview();
  expect(button('Create').disabled).toBe(false);
  button('Create').click();
  await settle();
  return vi.mocked(resources.apply).mock.calls[0][0];
}
async function reload() {
  await unmount(component!);
  component = undefined;
  await open();
}

// These tests mount the real wizard and durable-attempt helper; only network and
// session storage are isolated. A failed acknowledgement never authorizes replay.
describe('template durable Apply review', () => {
  it('persists the original identity before dispatch, preserves exact intent and recovers a lost acknowledgement without another Apply', async () => {
    vi.mocked(resources.apply).mockImplementation(async (request) => {
      const saved = [...entries.values()].map((value) => JSON.parse(value));
      expect(saved).toHaveLength(1);
      expect(saved[0]).toMatchObject({ requestId: request.requestId, target: { apiVersion: definition.summary.apiVersion, kind: definition.summary.kind, name: 'chosen-machine' }, scope: { source: 'template', template: templateName } });
      throw new Error('acknowledgement dropped');
    });
    const request = await createChosen();
    expect(request).toEqual({ resource: renderResource({ name: 'chosen-machine', password: 'literal-secret-do-not-store' }), createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false, requestId: expect.stringMatching(/^[0-9a-f-]{36}$/) });
    expect(target.textContent).toContain(request.requestId);
    expect(target.textContent).toContain('acknowledgement dropped');
    expect(button('Create').disabled).toBe(true);
    expect(operations.byRequest).not.toHaveBeenCalled();
    expect([...entries.values()].join('')).not.toContain('literal-secret-do-not-store');
    expect([...entries.values()].join('')).not.toContain('spec');
    vi.mocked(operations.byRequest).mockResolvedValue(operation(request.requestId!, 'running'));
    button('Recover Apply request').click();
    await settle();
    expect(operations.byRequest).toHaveBeenCalledWith(request.requestId, true, { signal: expect.any(AbortSignal) });
    expect(target.textContent).toContain('running');
    expect(target.textContent).toContain('apply-operation');
    expect(resources.apply).toHaveBeenCalledTimes(1);
  });

  it('does not navigate or claim application success from an acceptance acknowledgement', async () => {
    const previousHash = window.location.hash;
    vi.mocked(resources.apply).mockImplementation(async (request) => ({ operationId: 'accepted-operation', requestId: request.requestId }));
    await createChosen();
    expect(window.location.hash).toBe(previousHash);
    expect(target.querySelector('h2')?.textContent).toBe('Machine template');
    expect(target.textContent).toContain('accepted-operation');
    expect(target.textContent).not.toContain('succeeded');
    expect(button('Create').disabled).toBe(true);
  });

  it('reloads without secret parameters or consent and keeps the restored target independent of new defaults', async () => {
    const request = await createChosen();
    await reload();
    expect(input('name').value).toBe('default-machine');
    expect(input('password').value).toBe('');
    expect(target.textContent).toContain('chosen-machine');
    expect(target.textContent).toContain(request.requestId);
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(operations.byRequest).not.toHaveBeenCalled();
    vi.mocked(operations.byRequest).mockRejectedValue(new ApiError(404, 'unknown request'));
    button('Recover Apply request').click();
    await settle();
    expect(target.textContent).toContain(request.requestId);
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).toHaveBeenCalledTimes(1);
  });

  it('retains read-only recovery when a restored template is no longer available', async () => {
    const request = await createChosen();
    vi.mocked(templates.get).mockRejectedValue(new Error('template was removed'));
    await reload();
    expect(target.textContent).toContain('template was removed');
    expect(target.textContent).toContain(request.requestId);
    expect(target.textContent).toContain('chosen-machine');
    vi.mocked(operations.byRequest).mockResolvedValue(operation(request.requestId!));
    button('Recover Apply request').click();
    await settle();
    expect(target.textContent).toContain('provider failed');
    expect(resources.apply).toHaveBeenCalledTimes(1);
  });

  it.each(['parameters', 'template', 'role', 'authority', 'unmount'])('cannot dispatch a reviewed render when %s changes during asynchronous binding', async (change) => {
    await open();
    const binding = Promise.withResolvers<PingResponse>();
    vi.mocked(ping.ping).mockReturnValueOnce(binding.promise);
    button('Create').click();
    await settle();
    if (change === 'parameters') setParam('name', 'changed-name');
    if (change === 'template') selection.set('other-template');
    if (change === 'role') auth.set({ kind: 'signed-in', me: { ...me, role: 'viewer' } });
    if (change === 'authority') recordAuthority('gitops');
    if (change === 'unmount') { await unmount(component!); component = undefined; }
    await settle();
    binding.resolve(controller);
    await preview();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(entries.size).toBe(0);
  });

  it.each(['legacy', 'storage', 'viewer', 'gitops'])('prevents mutation for %s even with a valid template preview', async (scenario) => {
    if (scenario === 'legacy') vi.mocked(ping.ping).mockResolvedValue({ ...controller, supportsIdempotentApply: false });
    if (scenario === 'storage') vi.spyOn(sessionStorage, 'setItem').mockImplementation(() => { throw new Error('storage denied'); });
    if (scenario === 'viewer') auth.set({ kind: 'signed-in', me: { ...me, role: 'viewer' } });
    if (scenario === 'gitops') recordAuthority('gitops');
    await open();
    button('Create').click();
    await settle();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(entries.size).toBe(0);
    if (scenario !== 'storage') expect(button('Create').disabled).toBe(true);
    if (scenario === 'storage') expect(target.textContent).toContain('storage');
  });

  it.each(['controller', 'principal', 'capability', 'storage'])('validates %s before exposing a restored private identity', async (scenario) => {
    const request = await createChosen();
    await unmount(component!);
    component = undefined;
    if (scenario === 'controller') vi.mocked(ping.ping).mockResolvedValue({ ...controller, controllerId: 'another-controller' });
    if (scenario === 'principal') {
      const another = { ...me, principalId: 'another-actor' };
      auth.set({ kind: 'signed-in', me: another });
      vi.mocked(session.whoami).mockResolvedValue(another);
    }
    if (scenario === 'capability') vi.mocked(ping.ping).mockResolvedValue({ ...controller, supportsIdempotentApply: false });
    if (scenario === 'storage') vi.spyOn(sessionStorage, 'getItem').mockImplementation(() => { throw new Error('storage denied'); });
    await open();
    expect(target.textContent).not.toContain(request.requestId);
    expect(operations.byRequest).not.toHaveBeenCalled();
    expect(resources.apply).toHaveBeenCalledTimes(1);
  });

  it('aborts obsolete template loads and ignores their late defaults', async () => {
    const loaded = Promise.withResolvers<GetTemplateResponse>();
    vi.mocked(templates.get).mockReturnValueOnce(loaded.promise);
    await open();
    const signal = vi.mocked(templates.get).mock.calls[0][1]?.signal;
    selection.set('replacement-template');
    await preview();
    expect(signal?.aborted).toBe(true);
    loaded.resolve({
      ...definition,
      summary: { ...definition.summary, displayName: 'Obsolete template' },
      parameters: [{ name: 'name', type: 'string', defaultJson: '"obsolete-name"' }],
    });
    await settle();
    expect(target.querySelector('h2')?.textContent).toBe('Machine template');
    expect(input('name').value).toBe('default-machine');
    expect(target.textContent).not.toContain('obsolete-name');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('keeps stale renders and previews out of the current review and aborts their reads', async () => {
    const rendered = Promise.withResolvers<RenderTemplateResponse>();
    vi.mocked(templates.render).mockReturnValueOnce(rendered.promise);
    await open();
    const oldSignal = vi.mocked(templates.render).mock.calls[0][2]?.signal;
    setParam('name', 'current-machine');
    await preview();
    expect(oldSignal?.aborted).toBe(true);
    rendered.resolve({ resource: renderResource({ name: 'stale-machine' }) });
    await settle();
    expect(target.textContent).not.toContain('stale-machine');
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(vi.mocked(resources.apply).mock.calls[0][0].resource.metadata?.name).toBe('current-machine');
  });

  it('aborts obsolete dry-run reads and cannot turn a late old preview into current approval', async () => {
    const plan = Promise.withResolvers<DryRunApplyResponse>();
    vi.mocked(resources.dryRunApply).mockReturnValueOnce(plan.promise);
    await open();
    const signal = vi.mocked(resources.dryRunApply).mock.calls[0][1]?.signal;
    setParam('name', 'current-machine');
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive'] });
    await preview();
    expect(signal?.aborted).toBe(true);
    plan.resolve({ operation: 'create', summary: 'Obsolete no-gate approval' });
    await settle();
    expect(target.textContent).not.toContain('Obsolete no-gate approval');
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each(['Apply', 'recovery'])('ignores late %s responses after the template route changes', async (phase) => {
    const ack = Promise.withResolvers<ApplyResponse>();
    const recovered = Promise.withResolvers<Operation>();
    if (phase === 'Apply') vi.mocked(resources.apply).mockReturnValueOnce(ack.promise);
    const request = await createChosen();
    if (phase === 'recovery') {
      vi.mocked(operations.byRequest).mockReturnValueOnce(recovered.promise);
      button('Recover Apply request').click();
      await settle();
    }
    const signal = phase === 'Apply'
      ? vi.mocked(resources.apply).mock.calls[0][1]?.signal
      : vi.mocked(operations.byRequest).mock.calls[0][2]?.signal;
    selection.set('other-template');
    await preview();
    expect(signal?.aborted).toBe(true);
    ack.resolve({ requestId: request.requestId, operationId: 'late-ack-operation' });
    recovered.resolve(operation(request.requestId!, 'succeeded'));
    await settle();
    expect(target.textContent).not.toContain(request.requestId);
    expect(target.textContent).not.toContain('late-ack-operation');
    expect(target.textContent).not.toContain('succeeded');
    expect(input('password').value).toBe('');
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect([...entries.values()].map((value) => JSON.parse(value)).every((record) => !record.operationId)).toBe(true);
  });

  it('starts a genuinely new review only after terminal recovery, with a fresh render, preview, key and explicit gate consent', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive', 'i_know_this_breaks_the_cluster'] });
    await open();
    setParam('name', 'chosen-machine');
    await preview();
    const gates = () => [...target.querySelectorAll<HTMLInputElement>('article.diag input[type="checkbox"]')];
    for (const gate of gates()) gate.click();
    await settle();
    expect(button('Create').disabled).toBe(false);
    button('Create').click();
    await settle();
    const original: ApplyRequest = vi.mocked(resources.apply).mock.calls[0][0];
    expect(original.allowDestructive).toBe(true);
    expect(original.iKnowThisBreaksTheCluster).toBe(true);
    await reload();
    setParam('name', 'chosen-machine');
    await preview();
    expect(gates().every((gate) => !gate.checked)).toBe(true);
    expect(button('Create').disabled).toBe(true);
    vi.mocked(operations.byRequest).mockResolvedValue(operation(original.requestId!));
    button('Recover Apply request').click();
    await settle();
    const renders = vi.mocked(templates.render).mock.calls.length;
    const previews = vi.mocked(resources.dryRunApply).mock.calls.length;
    button('Review new Apply').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(button('Create').disabled).toBe(true);
    await preview();
    expect(templates.render).toHaveBeenCalledTimes(renders + 1);
    expect(resources.dryRunApply).toHaveBeenCalledTimes(previews + 1);
    expect(gates().every((gate) => !gate.checked)).toBe(true);
    expect(button('Create').disabled).toBe(true);
    for (const gate of gates()) gate.click();
    await settle();
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(2);
    const next = vi.mocked(resources.apply).mock.calls[1][0];
    expect(next.requestId).not.toBe(original.requestId);
    expect(next.resource).toEqual(renderResource({ name: 'chosen-machine' }));
    expect(next.createOnly).toBe(true);
    expect(next.allowDestructive).toBe(true);
    expect(next.iKnowThisBreaksTheCluster).toBe(true);
  });
});

it('shows coded reference paths and remaining diagnostics once without admitting template work', async () => {
  const diagnostic = 'Referenced VM cannot be read; inspect provider service health';
  vi.mocked(resources.dryRunApply).mockResolvedValue({
    operation: 'create',
    fieldErrors: [{ path: 'spec.target.host', code: 'provider_unavailable', message: diagnostic }],
    validationErrors: [`spec.target.host: ${diagnostic}`, 'Other provider precondition failed'],
  });
  await open();
  const panel = target.querySelector('[aria-label="Preview validation errors"]')!;
  expect(panel.textContent).toContain('spec.target.host');
  expect(panel.textContent).toContain('provider_unavailable');
  expect(panel.textContent?.split(diagnostic)).toHaveLength(2);
  expect(panel.textContent).toContain('Other provider precondition failed');
  expect(button('Create').disabled).toBe(true);
  expect(resources.apply).not.toHaveBeenCalled();
  expect(entries.size).toBe(0);
});
