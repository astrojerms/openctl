import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get, writable, type Writable } from 'svelte/store';
import EditSurface from './EditSurface.test.svelte';
import EditHarness from './EditHarness.test.svelte';
import { ApiError, operations, ping, resources, schemas, session, type DryRunApplyResponse, type GetFormSchemaResponse, type GetResourceResponse, type ListResourcesResponse, type Operation, type PingResponse, type Resource } from '../lib/api';
import { applyAttemptKey, type ApplyAttemptRecord, type ApplyAttemptScope } from '../lib/applyAttempt';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { ops } from '../lib/ops';
import { clearOpsDrawerFocus, opsDrawerFocus } from '../lib/opsDrawer';
import { saveRetry } from '../lib/retry';
import { parseYAML } from '../lib/yaml';
import * as router from '../lib/router';

vi.mock('../components/MonacoEditor.svelte', () => ({ default: EditSurface }));
vi.mock('../components/MonacoDiffEditor.svelte', () => ({ default: EditSurface }));

interface EditorRoute { apiVersion: string; kind: string; resourceName: string; reconcile?: boolean }
const createRoute: EditorRoute = { apiVersion: 'test.openctl.io/v1', kind: 'Machine', resourceName: '' };
const existingRoute: EditorRoute = { ...createRoute, resourceName: 'existing-machine' };
const createScope: ApplyAttemptScope = { source: 'create', apiVersion: createRoute.apiVersion, kind: createRoute.kind };
const me = { role: 'admin', userId: 'display-name', principalId: 'canonical-principal', sessionId: 'session-one' };
const controller: PingResponse = { echo: '', serverVersion: 'dev', controllerId: 'controller-one', supportsIdempotentApply: true };
const baseline: Resource = { apiVersion: createRoute.apiVersion, kind: createRoute.kind, metadata: { name: existingRoute.resourceName, labels: { preserve: 'label' } }, spec: { image: 'applied-image' } };
const desired: Resource = {
  apiVersion: createRoute.apiVersion, kind: createRoute.kind,
  metadata: { name: 'first-create', labels: { purpose: 'keep' }, annotations: { note: 'keep raw metadata', 'openctl.io/allow-destructive': 'true' } },
  spec: { password: { $secret: { file: 'machine-password' } }, target: { $ref: { kind: 'Host', name: 'host-one', path: 'address' } }, inline: 'never-store-this-literal', image: 'reviewed-image' },
};
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;
let route: Writable<EditorRoute>;

beforeEach(() => {
  vi.useFakeTimers();
  const entries = new Map<string, string>();
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => { entries.set(key, value); },
    removeItem: (key: string) => { entries.delete(key); },
    clear: () => entries.clear(),
    key: (index: number) => [...entries.keys()][index] ?? null,
    get length() { return entries.size; },
  } satisfies Storage);
  vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('Unexpected HTTP in isolated editor test'); }));
  target = document.createElement('div');
  document.body.append(target);
  route = writable({ ...createRoute });
  auth.set({ kind: 'signed-in', me });
  recordAuthority('mirror');
  ops.set([]);
  vi.spyOn(ping, 'ping').mockResolvedValue(controller);
  vi.spyOn(session, 'whoami').mockResolvedValue(me);
  vi.spyOn(schemas, 'getForm').mockResolvedValue({});
  vi.spyOn(schemas, 'validate').mockResolvedValue({ errors: [] });
  vi.spyOn(resources, 'list').mockResolvedValue({ resources: [] });
  vi.spyOn(resources, 'get').mockResolvedValue({ resource: baseline, applied: baseline });
  vi.spyOn(resources, 'dryRunApply').mockResolvedValue({ operation: 'create', requiredGates: [] });
  vi.spyOn(resources, 'apply').mockImplementation(async (request) => ({ requestId: request.requestId, operationId: 'accepted-operation' }));
  vi.spyOn(operations, 'get').mockRejectedValue(new Error('Unexpected retry lookup'));
  vi.spyOn(operations, 'byRequest').mockRejectedValue(new Error('Unexpected request recovery'));
  vi.spyOn(router, 'navigate').mockImplementation(() => {});
});

afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  ops.set([]);
  clearOpsDrawerFocus();
  resetAuthority();
  auth.set({ kind: 'unknown' });
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function settle() {
  for (let i = 0; i < 12; i++) { await Promise.resolve(); await tick(); }
}
async function preview() {
  await vi.advanceTimersByTimeAsync(360);
  await settle();
}
async function open(props: EditorRoute = createRoute) {
  route.set({ ...props });
  component = mount(EditHarness, { target, props: { route } });
  await settle();
  await preview();
}
function button(label: string): HTMLButtonElement {
  const found = [...target.querySelectorAll<HTMLButtonElement>('button')].find((node) => node.textContent?.trim() === label);
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}
function editor(): HTMLTextAreaElement {
  const found = target.querySelector<HTMLTextAreaElement>('textarea[aria-label="Manifest editor"]');
  if (!found) throw new Error('Missing manifest editor');
  return found;
}
async function edit(resource: Resource) {
  editor().value = JSON.stringify(resource);
  editor().dispatchEvent(new Event('input', { bubbles: true }));
  await settle();
}
function gates(): HTMLInputElement[] {
  return [...target.querySelectorAll<HTMLInputElement>('.gates input[type="checkbox"]')];
}
async function confirmGates() {
  for (const gate of gates()) if (!gate.checked) gate.click();
  await settle();
}
function record(scope: ApplyAttemptScope = createScope): ApplyAttemptRecord {
  return JSON.parse(sessionStorage.getItem(applyAttemptKey(scope))!);
}
function recovered(requestId: string, resource: Resource = desired, status = 'failed'): Operation {
  return { id: 'accepted-operation', type: 'apply', status, requestId, apiVersion: resource.apiVersion, kind: resource.kind, resourceName: resource.metadata.name, error: status === 'failed' ? 'provider failed after admission' : undefined };
}

describe('recoverable editor Apply', () => {
  it('reapplies unchanged raw intent only through explicit reconciliation and retains durable recovery guards', async () => {
    const applied: Resource = { ...baseline, spec: { host: { $ref: { apiVersion: 'test.openctl.io/v1', kind: 'Host', name: 'guest', field: 'status.ip' } }, password: { $secret: { provider: 'file', key: 'password' } } } };
    vi.mocked(resources.get).mockResolvedValue({ applied, resource: { ...applied, spec: { host: '192.0.2.8', password: 'resolved-value-must-not-submit' } } });
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'no-op', requiredGates: [] });
    await open(existingRoute);
    expect(button('Apply').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
    await unmount(component!); component = undefined;
    await open({ ...existingRoute, reconcile: true });
    expect(resources.apply).not.toHaveBeenCalled();
    expect(button('Reconcile').disabled).toBe(false);
    button('Reconcile').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
    const request = vi.mocked(resources.apply).mock.calls[0][0];
    expect(request.resource).toEqual(applied);
    expect(request.createOnly).not.toBe(true);
    expect(request.allowDestructive).toBe(false);
    expect(request.iKnowThisBreaksTheCluster).toBe(false);
    expect(request.requestId).toMatch(/^[0-9a-f-]{36}$/);
    expect(button('Reconcile').disabled).toBe(true);
    button('Reconcile').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
  });

  it('sends exactly the reviewed desired resource and explicit flags after persisting metadata only', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive', 'i_know_this_breaks_the_cluster'] });
    vi.mocked(resources.apply).mockImplementation(async (request) => {
      expect(record()).toEqual({
        version: 1, binding: { controllerId: controller.controllerId, userId: me.principalId }, scope: createScope,
        target: { apiVersion: desired.apiVersion, kind: desired.kind, name: desired.metadata.name }, requestId: request.requestId,
      });
      return { requestId: request.requestId, operationId: 'accepted-operation' };
    });
    await open();
    await edit(desired);
    await preview();
    expect(button('Create').disabled).toBe(true);
    await confirmGates();
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledExactlyOnceWith({
      resource: desired, createOnly: true, allowDestructive: true, iKnowThisBreaksTheCluster: true, requestId: expect.any(String),
    }, { signal: expect.any(AbortSignal) });
    expect(record().requestId).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.stringify(record())).not.toContain('never-store-this-literal');
    expect(JSON.stringify(record())).not.toContain('machine-password');
    expect(JSON.stringify(record())).not.toContain('allowDestructive');
    expect(vi.mocked(schemas.validate).mock.lastCall?.[0]).toEqual(desired);
    expect(vi.mocked(resources.dryRunApply).mock.lastCall?.[0]).toEqual(desired);
    expect(button('Create').disabled).toBe(true);
  });

  it('submits the exact form-reviewed resource without resolving markers or dropping metadata', async () => {
    vi.mocked(schemas.getForm).mockResolvedValue({ json: JSON.stringify({ type: 'any' }) });
    await open();
    button('Form').click();
    await settle();
    const form = target.querySelector<HTMLTextAreaElement>('textarea[placeholder="freeform (JSON / YAML)"]');
    expect(form).not.toBeNull();
    form!.value = JSON.stringify(desired);
    form!.dispatchEvent(new Event('input', { bubbles: true }));
    await settle();
    await preview();
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledExactlyOnceWith({
      resource: desired, createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false, requestId: expect.any(String),
    }, { signal: expect.any(AbortSignal) });
  });

  it('keeps a lost acknowledgement recovery-only and recovers read-only without another Apply', async () => {
    vi.mocked(resources.apply).mockRejectedValue(new Error('acknowledgement dropped'));
    vi.mocked(operations.byRequest).mockImplementation(async (requestId) => recovered(requestId));
    await open();
    await edit(desired);
    await preview();
    button('Create').click();
    await settle();
    const requestId = record().requestId;
    expect(target.textContent).toContain(requestId);
    expect(target.textContent).toContain('acknowledgement dropped');
    expect(operations.byRequest).not.toHaveBeenCalled();
    expect(button('Create').disabled).toBe(true);
    await edit({ ...desired, spec: { image: 'another-draft' } });
    await preview();
    button('Create').click();
    button('Recover Apply request').click();
    await settle();
    expect(operations.byRequest).toHaveBeenCalledExactlyOnceWith(requestId, true, { signal: expect.any(AbortSignal) });
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(target.textContent).toContain('provider failed after admission');
    expect(target.textContent).toContain('accepted-operation');
    expect(button('Create').disabled).toBe(true);
    button('Inspect Apply operation').click();
    expect(get(opsDrawerFocus)?.opId).toBe('accepted-operation');
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it('restores only the original request on reload, separate from the fresh create draft and confirmations', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive'] });
    await open();
    await edit(desired);
    await preview();
    await confirmGates();
    button('Create').click();
    await settle();
    const original = record();
    await unmount(component!);
    component = undefined;
    await open();
    expect(record()).toEqual(original);
    expect(target.textContent).toContain(original.requestId);
    expect(target.textContent).toContain('Machine/first-create');
    expect(parseYAML(editor().value).doc.metadata?.name).not.toBe(desired.metadata.name);
    expect(editor().value).not.toContain('never-store-this-literal');
    expect(editor().value).not.toContain('$secret');
    expect(gates()).toHaveLength(1);
    expect(gates()[0].checked).toBe(false);
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(operations.byRequest).not.toHaveBeenCalled();
  });

  it('retains request recovery when loading the original resource fails', async () => {
    const scope: ApplyAttemptScope = { source: 'resource', apiVersion: existingRoute.apiVersion, kind: existingRoute.kind, name: existingRoute.resourceName };
    const saved: ApplyAttemptRecord = {
      version: 1, scope, binding: { controllerId: controller.controllerId!, userId: me.principalId },
      target: { apiVersion: baseline.apiVersion, kind: baseline.kind, name: baseline.metadata.name }, requestId: 'lost-before-reload',
    };
    sessionStorage.setItem(applyAttemptKey(scope), JSON.stringify(saved));
    vi.mocked(resources.get).mockRejectedValue(new ApiError(503, 'resource inspection unavailable'));
    vi.mocked(operations.byRequest).mockImplementation(async (requestId) => recovered(requestId, baseline));
    await open(existingRoute);
    expect(target.textContent).toContain('resource inspection unavailable');
    expect(target.textContent).toContain(saved.requestId);
    button('Recover Apply request').click();
    await settle();
    expect(target.textContent).toContain('provider failed after admission');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each(['old controller', 'storage write failure', 'storage read failure'])('fails closed for %s', async (failure) => {
    if (failure === 'old controller') vi.mocked(ping.ping).mockResolvedValue({ ...controller, supportsIdempotentApply: false });
    if (failure === 'storage write failure') vi.spyOn(sessionStorage, 'setItem').mockImplementation(() => { throw new Error('storage quota exceeded'); });
    if (failure === 'storage read failure') vi.spyOn(sessionStorage, 'getItem').mockImplementation(() => { throw new Error('storage access denied'); });
    await open();
    button('Create').click();
    await settle();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(target.textContent).toContain(failure === 'old controller' ? 'Upgrade' : failure === 'storage write failure' ? 'storage quota exceeded' : 'storage access denied');
  });

  it.each(['viewer', 'gitops', 'unknown authority', 'missing canonical principal', 'owned resource', 'unknown gate', 'name collision'])('blocks sends for %s', async (policy) => {
    if (policy === 'viewer') auth.set({ kind: 'signed-in', me: { ...me, role: 'viewer' } });
    if (policy === 'gitops') recordAuthority('gitops');
    if (policy === 'unknown authority') resetAuthority();
    if (policy === 'missing canonical principal') auth.set({ kind: 'signed-in', me: { ...me, principalId: undefined } });
    if (policy === 'owned resource') vi.mocked(resources.get).mockResolvedValue({ resource: { ...baseline, metadata: { ...baseline.metadata, ownerRefs: [{ apiVersion: createRoute.apiVersion, kind: 'Cluster', name: 'owner' }] } }, applied: baseline });
    if (policy === 'unknown gate') vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['unrecognized_authorization'] });
    if (policy === 'name collision') vi.mocked(resources.list).mockResolvedValue({ resources: [desired] });
    await open(policy === 'owned resource' ? existingRoute : createRoute);
    if (policy === 'name collision') { await edit(desired); await preview(); }
    await confirmGates();
    expect(button(policy === 'owned resource' ? 'Apply' : 'Create').disabled).toBe(true);
    button(policy === 'owned resource' ? 'Apply' : 'Create').click();
    await settle();
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each(['editor', 'gate', 'role', 'authority', 'route'])('rejects a changed %s while the final capability check is pending', async (change) => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive'] });
    await open();
    await edit(desired);
    await preview();
    await confirmGates();
    const binding = Promise.withResolvers<PingResponse>();
    vi.mocked(ping.ping).mockReturnValueOnce(binding.promise);
    button('Create').click();
    await settle();
    if (change === 'editor') await edit({ ...desired, spec: { image: 'changed-during-review' } });
    if (change === 'gate') { gates()[0].click(); await settle(); }
    if (change === 'role') { auth.set({ kind: 'signed-in', me: { ...me, role: 'viewer' } }); await settle(); }
    if (change === 'authority') { recordAuthority('gitops'); await settle(); }
    if (change === 'route') { route.set({ ...createRoute, kind: 'OtherMachine' }); await settle(); }
    binding.resolve(controller);
    await settle();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(applyAttemptKey(createScope))).toBeNull();
    if (change === 'route') expect(parseYAML(editor().value).doc.kind).toBe('OtherMachine');
  });

  it('ignores aborted schema, name-list and preview completions from the previous route', async () => {
    const schema = Promise.withResolvers<GetFormSchemaResponse>();
    const names = Promise.withResolvers<ListResourcesResponse>();
    const oldPreview = Promise.withResolvers<DryRunApplyResponse>();
    vi.mocked(schemas.getForm).mockReturnValueOnce(schema.promise);
    vi.mocked(resources.list).mockReturnValueOnce(names.promise);
    vi.mocked(resources.dryRunApply).mockReturnValueOnce(oldPreview.promise);
    await open();
    const schemaSignal = vi.mocked(schemas.getForm).mock.calls[0][2]?.signal;
    const namesSignal = vi.mocked(resources.list).mock.calls[0][2]?.signal;
    const previewSignal = vi.mocked(resources.dryRunApply).mock.calls[0][1]?.signal;
    route.set({ ...createRoute, kind: 'OtherMachine' });
    await settle();
    await preview();
    const freshDraft = editor().value;
    const freshName = parseYAML(freshDraft).doc.metadata!.name;
    schema.resolve({ json: JSON.stringify({ type: 'object', fields: [{ name: 'spec', type: 'object', fields: [{ name: 'image', type: 'string', default: 'stale-schema-image' }] }] }) });
    names.resolve({ resources: [{ ...desired, metadata: { name: freshName } }] });
    oldPreview.resolve({ operation: 'update', requiredGates: ['allow_destructive'], summary: 'stale previous preview' });
    await settle();
    expect(schemaSignal?.aborted).toBe(true);
    expect(namesSignal?.aborted).toBe(true);
    expect(previewSignal?.aborted).toBe(true);
    expect(editor().value).toBe(freshDraft);
    expect(target.textContent).not.toContain('stale previous preview');
    expect(gates()).toHaveLength(0);
    expect(button('Create').disabled).toBe(false);
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(vi.mocked(resources.apply).mock.calls[0][0].resource).toEqual(parseYAML(freshDraft).doc);
  });

  it('rejects events from an editor and confirmation belonging to a departed route', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive'] });
    await open();
    const previousEditor = editor();
    const previousGate = gates()[0];
    route.set({ ...createRoute, kind: 'OtherMachine' });
    await settle();
    await preview();
    const freshDraft = editor().value;
    previousEditor.value = JSON.stringify(desired);
    previousEditor.dispatchEvent(new Event('input', { bubbles: true }));
    previousGate.checked = true;
    previousGate.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect(editor().value).toBe(freshDraft);
    expect(gates()[0].checked).toBe(false);
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('ignores an existing-resource load that completes after changing the route', async () => {
    const previous = Promise.withResolvers<GetResourceResponse>();
    vi.mocked(resources.get).mockReturnValueOnce(previous.promise);
    await open(existingRoute);
    const signal = vi.mocked(resources.get).mock.calls[0][3]?.signal;
    route.set({ ...createRoute, kind: 'OtherMachine' });
    await settle();
    await preview();
    const freshDraft = editor().value;
    previous.resolve({ resource: baseline, applied: baseline });
    await settle();
    expect(signal?.aborted).toBe(true);
    expect(editor().value).toBe(freshDraft);
    expect(parseYAML(editor().value).doc.kind).toBe('OtherMachine');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('requires fresh preview and cleared gates after a terminal first-create recovery, then uses a fresh create-only key', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive', 'i_know_this_breaks_the_cluster'] });
    vi.mocked(operations.byRequest).mockImplementation(async (requestId) => recovered(requestId));
    await open();
    await edit(desired);
    await preview();
    await confirmGates();
    button('Create').click();
    await settle();
    const original = record();
    button('Recover Apply request').click();
    await settle();
    const previousSchemas = vi.mocked(schemas.getForm).mock.calls.length;
    const previousPreviews = vi.mocked(resources.dryRunApply).mock.calls.length;
    button('Review new Apply').click();
    await settle();
    expect(record().requestId).toBe(original.requestId);
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(button('Create').disabled).toBe(true);
    expect(vi.mocked(schemas.getForm).mock.calls.length).toBeGreaterThan(previousSchemas);
    await preview();
    expect(vi.mocked(resources.dryRunApply).mock.calls.length).toBeGreaterThan(previousPreviews);
    expect(gates().every((gate) => !gate.checked)).toBe(true);
    expect(button('Create').disabled).toBe(true);
    await confirmGates();
    button('Create').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(2);
    const replacement = vi.mocked(resources.apply).mock.calls[1][0];
    expect(replacement.resource).toEqual(desired);
    expect(replacement.createOnly).toBe(true);
    expect(replacement.requestId).not.toBe(original.requestId);
    expect(replacement.allowDestructive).toBe(true);
    expect(replacement.iKnowThisBreaksTheCluster).toBe(true);
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it('uses createOnly for an explicit failed-operation retry without a managed or observed baseline', async () => {
    const failed: Operation = { ...recovered('previous-request'), id: 'failed-first-apply', manifestJson: JSON.stringify(desired) };
    saveRetry(sessionStorage, failed);
    vi.mocked(operations.get).mockResolvedValue(failed);
    vi.mocked(resources.get).mockRejectedValue(new ApiError(404, 'no managed baseline'));
    await open({ ...createRoute, resourceName: desired.metadata.name });
    button('Apply').click();
    await settle();
    const expected = { ...desired, metadata: { ...desired.metadata, annotations: { note: 'keep raw metadata' } } };
    expect(resources.apply).toHaveBeenCalledExactlyOnceWith({ resource: expected, createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false, requestId: expect.any(String) }, { signal: expect.any(AbortSignal) });
    expect(vi.mocked(resources.apply).mock.calls[0][0].requestId).not.toBe('previous-request');
  });

  it('does not replace the applied baseline or navigate merely because Apply was acknowledged or succeeded', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({ operation: 'update', requiredGates: [] });
    await open(existingRoute);
    const draft = { ...baseline, spec: { image: 'not-yet-applied' } };
    await edit(draft);
    await preview();
    button('Apply').click();
    await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(vi.mocked(resources.apply).mock.calls[0][0].createOnly).toBeUndefined();
    expect(button('Diff vs applied').disabled).toBe(false);
    button('Diff vs applied').click();
    await settle();
    const applied = target.querySelector('[aria-label="Applied baseline"]');
    expect(parseYAML(applied!.textContent!).doc).toEqual(baseline);
    const request = vi.mocked(resources.apply).mock.calls[0][0];
    const succeeded = recovered(request.requestId!, baseline, 'succeeded');
    ops.set([succeeded]);
    await settle();
    await vi.advanceTimersByTimeAsync(1000);
    expect(router.navigate).not.toHaveBeenCalled();
    expect(target.querySelector('h2')?.textContent).toBe(`Edit ${existingRoute.resourceName}`);
    expect(parseYAML(applied!.textContent!).doc).toEqual(baseline);
    button('Discard').click();
    await settle();
    expect(button('Diff vs applied').disabled).toBe(true);
    expect(parseYAML(editor().value).doc).toEqual(baseline);
  });
});

describe('reference diagnostic inspection', () => {
  const reference = { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'guest #1' };

  it('links only the declared dependency and requested output, deduplicates plain errors, and never admits blocked work', async () => {
    const message = 'The requested guest address is unavailable';
    vi.mocked(resources.dryRunApply).mockResolvedValue({
      operation: 'create',
      fieldErrors: [{ path: 'spec.target.host', code: 'reference_field_unavailable', message, reference, referenceField: 'status.ip' }],
      validationErrors: [`spec.target.host: ${message}`, message, 'Other preview precondition failed'],
    });
    await open();
    await edit(desired);
    await preview();
    const panel = target.querySelector('[aria-label="Preview validation errors"]')!;
    const link = panel.querySelector('a')!;
    expect(link.textContent).toBe('Inspect referenced resource');
    expect(link.getAttribute('href')).toBe('#/k/proxmox.openctl.io/v1/VirtualMachine/guest%20%231');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noopener');
    expect(panel.textContent).toContain('VirtualMachine/guest #1');
    expect(panel.textContent).toContain('requested field: status.ip');
    expect(panel.textContent).toContain('spec.target.host');
    expect(panel.textContent).toContain('[reference_field_unavailable]');
    expect(panel.textContent?.split(message)).toHaveLength(2);
    expect(panel.textContent).toContain('Other preview precondition failed');
    expect(button('Create').disabled).toBe(true);
    button('Create').click();
    await settle();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(resources.get).not.toHaveBeenCalled();
    expect(router.navigate).not.toHaveBeenCalled();
    expect(sessionStorage.length).toBe(0);
  });

  it('shows whole-resource field-only failures and keeps inline form highlighting', async () => {
    vi.mocked(schemas.getForm).mockResolvedValue({ json: JSON.stringify({
      type: 'object', fields: [
        { name: 'apiVersion', type: 'string' }, { name: 'kind', type: 'string' },
        { name: 'metadata', type: 'object', fields: [{ name: 'name', type: 'string' }] },
        { name: 'spec', type: 'object', fields: [{ name: 'image', type: 'string' }] },
      ],
    }) });
    vi.mocked(resources.dryRunApply).mockResolvedValue({
      operation: 'create',
      fieldErrors: [{ path: 'spec.image', code: 'reference_resource_unavailable', message: 'Image dependency unavailable', reference }],
    });
    await open();
    await edit({ apiVersion: createRoute.apiVersion, kind: createRoute.kind, metadata: { name: 'blocked-machine' }, spec: { image: 'approved' } });
    await preview();
    button('Form').click();
    await settle();
    expect(target.querySelector('.row-error')?.textContent).toContain('[reference_resource_unavailable] Image dependency unavailable');
    const panel = target.querySelector('[aria-label="Preview validation errors"]')!;
    expect(panel.textContent).toContain('whole resource');
    expect(panel.textContent).not.toContain('requested field:');
    expect(panel.querySelector('a')?.getAttribute('href')).toBe('#/k/proxmox.openctl.io/v1/VirtualMachine/guest%20%231');
    expect(button('Create').disabled).toBe(true);
    expect(target.querySelector('.preview')).toBeNull();
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each([
    ['absent', undefined], ['null', null], ['empty', {}],
    ['missing version', { kind: 'VirtualMachine', name: 'guest' }],
    ['missing kind', { apiVersion: 'proxmox.openctl.io/v1', name: 'guest' }],
    ['missing name', { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine' }],
    ['empty name', { ...reference, name: '' }],
    ['whitespace name', { ...reference, name: ' ' }],
    ['non-string name', { ...reference, name: 7 }],
    ['invalid version', { ...reference, apiVersion: 'proxmox.openctl.io/v1/extra' }],
    ['unversioned', { ...reference, apiVersion: 'proxmox.openctl.io' }],
    ['create route collision', { ...reference, name: 'new' }],
    ['array', []],
  ])('keeps %s reference metadata as a plain blocking diagnostic without guessing a target', async (_label, malformed) => {
    vi.mocked(resources.dryRunApply).mockResolvedValue(JSON.parse(JSON.stringify({
      operation: 'create',
      fieldErrors: [{ path: 'spec.target.host', code: 'reference_resource_unavailable', message: 'Inspect VirtualMachine/guest at proxmox.openctl.io/v1', reference: malformed, referenceField: 'status.ip' }],
    })));
    await open();
    const panel = target.querySelector('[aria-label="Preview validation errors"]')!;
    expect(panel.textContent).toContain('Inspect VirtualMachine/guest at proxmox.openctl.io/v1');
    expect(panel.querySelector('a')).toBeNull();
    expect(button('Create').disabled).toBe(true);
    expect(resources.get).not.toHaveBeenCalled();
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each([{ referenceField: null }, { referenceField: 7 }, { referenceField: {} }, { referenceField: [] }])('does not reinterpret malformed output metadata $referenceField as a whole-resource reference', async ({ referenceField }) => {
    vi.mocked(resources.dryRunApply).mockResolvedValue(JSON.parse(JSON.stringify({
      operation: 'create',
      fieldErrors: [{ path: 'spec.target.host', message: 'Malformed reference output metadata', reference, referenceField }],
    })));
    await open();
    const panel = target.querySelector('[aria-label="Preview validation errors"]')!;
    expect(panel.textContent).toContain('Malformed reference output metadata');
    expect(panel.querySelector('a')).toBeNull();
    expect(button('Create').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each(['draft', 'route', 'session'])('drops reference diagnostics immediately when the %s changes', async (change) => {
    vi.mocked(resources.dryRunApply).mockResolvedValue({
      fieldErrors: [{ path: 'spec.target.host', message: 'Stale dependency evidence', reference, referenceField: 'status.ip' }],
    });
    await open();
    expect(target.querySelector('[aria-label="Preview validation errors"] a')).not.toBeNull();
    if (change === 'draft') await edit(desired);
    if (change === 'route') route.set({ ...createRoute, kind: 'OtherMachine' });
    if (change === 'session') auth.set({ kind: 'signed-out' });
    await settle();
    expect(target.querySelector('[aria-label="Preview validation errors"]')).toBeNull();
    expect(target.textContent).not.toContain('Stale dependency evidence');
    expect(resources.apply).not.toHaveBeenCalled();
  });
});
