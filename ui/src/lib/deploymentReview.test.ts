import { describe, expect, it, vi } from 'vitest';
import { ApiError, ping, session, UnauthorizedError, type ApplyRequest, type GetResourceResponse, type Operation, type Resource } from './api';
import { jellyfinDeployment, type JellyfinInputs } from './jellyfin';
import { APP_API, VM_API, REVIEW_KEYS, type DeploymentProfile, type DeploymentStep } from './deployment';
import { createSavedReview, DeploymentReview, discardSavedReview, loadReviewBinding, MAX_REVIEW_BYTES, readSavedReview, reviewError, type ReviewAPI, type ReviewConsent } from './deploymentReview';

const binding = { controllerId: 'controller-a', userId: 'operator-a' };
const resource: Resource = {
  apiVersion: APP_API, kind: 'Jellyfin', metadata: { name: 'media', labels: { purpose: 'media' } },
  spec: {
    target: { host: { $ref: { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', name: 'gpu', field: 'status.ip' } }, privateKeyFile: '/keys/id_ed25519', knownHostsFile: '/keys/known_hosts' },
    image: 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
    admin: { password: { $secret: { provider: 'file', key: 'admin-password' } } },
    nas: { credentials: { $secret: { provider: 'file', key: 'nas-credentials' } } },
  },
};
const step: DeploymentStep = { title: 'Install Jellyfin', resource, createOnly: true };
const consent: ReviewConsent = { dedicated: true, trustedKey: true, publicExposure: true, gates: {} };
const absent = () => new ApiError(404, 'not found');
const unprovenHealth: Resource['status'][] = [
  { phase: 'ready' },
  { phase: 'ready', ready: true, checks: { gpu: { state: 'not_ready' } } },
  { phase: 'ready', ready: true, checks: { gpu: { state: 'unknown' } } },
  { ready: true, checks: { gpu: { state: 'future' } } },
  { ready: true, checks: { gpu: { ready: true } } },
  { ready: true, checks: [] },
];
const guestStatus = {
  state: 'running', ip: '192.0.2.8',
  ipObservation: { state: 'available', code: 'guest_ip_available', message: 'Unique guest address observed; reachability unverified.' },
};
const guest: Resource = { apiVersion: VM_API, kind: 'VirtualMachine', metadata: { name: 'gpu' }, spec: {} };
function memoryStorage() {
  const data = new Map<string, string>();
  return {
    getItem: vi.fn((key: string) => data.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => { data.set(key, value); }),
    removeItem: vi.fn((key: string) => { data.delete(key); }),
  };
}
function harness(steps: DeploymentStep[] = [step], profile: DeploymentProfile = 'jellyfin') {
  const storage = memoryStorage();
  const ops = new Map<string, Operation>();
  const live = new Map<string, GetResourceResponse>();
  if (!steps.some((step) => step.resource.apiVersion === VM_API && step.resource.kind === 'VirtualMachine')) {
    live.set('gpu', { applied: guest, resource: { ...guest, status: guestStatus } });
  }
  let sequence = 0;
  const api = {
    binding: vi.fn(async () => binding),
    getOperation: vi.fn(async (id: string) => {
      const op = [...ops.values()].find((item) => item.id === id);
      if (!op) throw absent();
      return op;
    }),
    byRequest: vi.fn(async (key: string) => {
      const op = ops.get(key);
      if (!op) throw absent();
      return op;
    }),
    getResource: vi.fn(async (_apiVersion: string, _kind: string, name: string) => {
      const result = live.get(name);
      if (!result) throw absent();
      return result;
    }),
    validate: vi.fn(async () => ({})),
    preview: vi.fn<ReviewAPI['preview']>(async (intent) => ({ operation: live.has(intent.metadata!.name) ? 'update' : 'create', requiredGates: [] })),
    apply: vi.fn(async (request: ApplyRequest) => {
      const intent = request.resource as Resource;
      const op: Operation = {
        id: `op-${request.requestId}`, requestId: request.requestId, type: 'apply', status: 'running',
        apiVersion: intent.apiVersion, kind: intent.kind, resourceName: intent.metadata.name, manifestJson: JSON.stringify(intent),
      };
      ops.set(request.requestId!, op);
      return { operationId: op.id, requestId: request.requestId };
    }),
    watch: vi.fn<ReviewAPI['watch']>(async () => {}),
    requestId: vi.fn(() => `request-${++sequence}`),
  } satisfies ReviewAPI;
  const engine = new DeploymentReview(profile, storage, createSavedReview(profile, binding, steps), { canApply: () => true, api });
  const restore = () => new DeploymentReview(profile, storage, readSavedReview(profile, storage, binding)!, { canApply: () => true, api });
  const accept = (key = 'request-1', status = 'succeeded', intent = resource) => {
    const op = ops.get(key)!;
    ops.set(key, { ...op, status });
    live.set(intent.metadata.name, { applied: intent, resource: { ...intent, status: intent.apiVersion === VM_API ? guestStatus : { ready: true } } });
  };
  return { storage, engine, api, ops, live, restore, accept };
}

// All transitions use isolated storage and explicit fake controller responses;
// no tests start a controller, contact providers, or rely on stream timing.
describe('tab-local prepared review', () => {
  it('persists desired metadata/references but no previews, operation status, readiness or checkbox consent', async () => {
    const h = harness();
    h.engine.save();
    await h.engine.review();
    const saved = readSavedReview('jellyfin', h.storage, binding)!;
    expect(saved.steps[0].resource).toEqual(resource);
    expect(Object.keys(saved)).toEqual(['version', 'profile', 'binding', 'cursor', 'steps']);
    expect(Object.keys(saved.steps[0])).toEqual(['title', 'resource', 'createOnly']);
    const restored = h.restore();
    expect(restored.state.preview).toBeNull();
    expect(restored.state.observed).toBeNull();
    expect(restored.state.verified).toBe(false);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it.each([
    { controllerId: 'other', userId: binding.userId },
    { controllerId: binding.controllerId, userId: 'other' },
  ])('rejects a different controller or signed-in user: %j', (other) => {
    const h = harness(); h.engine.save();
    expect(() => readSavedReview('jellyfin', h.storage, other)).toThrow(/different controller or signed-in user/);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it.each(['not-json', 'null', '{}', JSON.stringify({ version: 0, binding, cursor: 0, steps: [step] }), ' '.repeat(MAX_REVIEW_BYTES + 1)])(
    'fails closed for invalid/versioned/oversized saved data', (raw) => {
      const h = harness(); h.storage.setItem(REVIEW_KEYS.jellyfin, raw);
      expect(() => readSavedReview('jellyfin', h.storage, binding)).toThrow();
      expect(h.api.apply).not.toHaveBeenCalled();
    },
  );

  it('rejects cached success or inherited consent fields instead of treating them as evidence', () => {
    const h = harness();
    h.storage.setItem(REVIEW_KEYS.jellyfin, JSON.stringify({ ...createSavedReview('jellyfin', binding, [step]), ready: true, publicConsent: true }));
    expect(() => readSavedReview('jellyfin', h.storage, binding)).toThrow(/malformed/);
  });

  it('removes inherited request-consent annotations without losing desired metadata or secret markers', () => {
    const declared = { ...resource, metadata: { ...resource.metadata, annotations: { note: 'keep', 'openctl.io/allow-destructive': 'true' } } };
    const saved = createSavedReview('jellyfin', binding, [{ ...step, resource: declared }]);
    expect(saved.steps[0].resource.metadata.annotations).toEqual({ note: 'keep' });
    expect(saved.steps[0].resource.spec).toEqual(resource.spec);
    expect(declared.metadata.annotations['openctl.io/allow-destructive']).toBe('true');
  });

  it('refuses expanded credentials and private-key bytes in browser recovery records', () => {
    expect(() => createSavedReview('jellyfin', binding, [{ ...step, resource: { ...resource, spec: { password: 'plaintext' } } }])).toThrow(/secret references/);
    expect(() => createSavedReview('jellyfin', binding, [{ ...step, resource: { ...resource, spec: { target: '-----BEGIN OPENSSH PRIVATE KEY-----' } } }])).toThrow(/credential material/);
  });

  it('discards only browser state without canceling operations or touching resources', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    const operationCount = h.ops.size;
    discardSavedReview('jellyfin', h.storage);
    expect(readSavedReview('jellyfin', h.storage, binding)).toBeNull();
    expect(h.ops.size).toBe(operationCount);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });
});

describe('durable Apply acknowledgement recovery', () => {
  it('recovers a lost ACK by request lookup with no second submission, including after reload', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    const apply = h.api.apply.getMockImplementation()!;
    h.api.apply.mockImplementation(async (request) => { await apply(request); throw new TypeError('connection reset'); });
    await h.engine.submit(consent);
    expect(h.engine.state.operation?.id).toBe('op-request-1');
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt?.operationId).toBe('op-request-1');
    const resumed = h.restore(); await resumed.resume();
    expect(resumed.state.operation?.status).toBe('running');
    expect(resumed.state.preview).toBeNull();
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });

  it('never replaces an uncertain key, and retransmits only on explicit fresh review/consent', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    const apply = h.api.apply.getMockImplementation()!;
    h.api.apply.mockRejectedValueOnce(new TypeError('lost request'));
    await expect(h.engine.submit(consent)).rejects.toThrow(/outcome is unknown/);
    const original = readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt!.requestId;
    const resumed = h.restore(); await resumed.resume();
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    await expect(resumed.submit(consent)).rejects.toThrow(/Refresh the preview/);
    await resumed.review();
    await expect(resumed.submit({ ...consent, dedicated: false })).rejects.toThrow(/Confirm/);
    h.api.apply.mockImplementation(apply);
    await resumed.submit(consent);
    expect(h.api.apply.mock.calls[1][0].requestId).toBe(original);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });

  it('persists identity before issuing Apply, and accepted operation ID before observation', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    const apply = h.api.apply.getMockImplementation()!;
    h.api.apply.mockImplementation(async (request) => {
      expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt?.requestId).toBe(request.requestId);
      return apply(request);
    });
    await h.engine.submit(consent);
    h.api.watch.mockImplementation(async () => {
      expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt?.operationId).toBe('op-request-1');
    });
    await expect(h.engine.observe()).rejects.toThrow(/monitoring disconnected/);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it('retains the original saved request if acknowledgement persistence fails after acceptance', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    const apply = h.api.apply.getMockImplementation()!;
    const write = h.storage.setItem.getMockImplementation()!;
    h.api.apply.mockImplementation(async (request) => {
      const accepted = await apply(request);
      h.storage.setItem.mockImplementation(() => { throw new Error('storage disabled'); });
      return accepted;
    });
    await expect(h.engine.submit(consent)).rejects.toThrow(/Cannot save/);
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt?.requestId).toBe('request-1');
    h.storage.setItem.mockImplementation(write);
    const resumed = h.restore(); await resumed.resume();
    expect(resumed.state.operation?.id).toBe('op-request-1');
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it('does not submit at all when saving the request identity fails', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.storage.setItem.mockImplementation(() => { throw new Error('quota exceeded'); });
    await expect(h.engine.submit(consent)).rejects.toThrow(/Cannot save/);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.engine.state.preview).toBeNull();
  });

  it.each([new ApiError(503, 'provider credentials'), new UnauthorizedError('expired'), new TypeError('offline')])(
    'blocks uncertain recovery during an outage instead of creating another request: %s', async (error) => {
      const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
      h.api.getOperation.mockRejectedValue(error);
      const resumed = h.restore();
      await expect(resumed.resume()).rejects.toBe(error);
      expect(resumed.state.verified).toBe(false);
      expect(h.api.apply).toHaveBeenCalledTimes(1);
    },
  );

  it('does not replace a pruned accepted operation when the durable ledger blocks lookup', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.api.getOperation.mockRejectedValue(absent());
    h.api.byRequest.mockRejectedValue(new ApiError(412, 'original operation pruned'));
    const resumed = h.restore(); await expect(resumed.resume()).rejects.toBeInstanceOf(ApiError);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });

  it('blocks an acknowledgement with a different request ID even if read-only lookup finds accepted work', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    const apply = h.api.apply.getMockImplementation()!;
    h.api.apply.mockImplementation(async (request) => ({ ...await apply(request), requestId: 'unrelated' }));
    await expect(h.engine.submit(consent)).rejects.toThrow(/acknowledgement did not match/);
    expect(h.engine.state.operation?.id).toBe('op-request-1');
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.watch).not.toHaveBeenCalled();
  });
});

describe('authoritative root and readiness validation', () => {
  it.each([
    { id: 'different' }, { parentId: 'parent' }, { type: 'delete' }, { requestId: 'other' },
    { apiVersion: 'other/v1' }, { kind: 'VirtualMachine' }, { resourceName: 'other' },
    { manifestJson: JSON.stringify({ ...resource, spec: { changed: true } }) }, { manifestJson: '{}' },
  ])('blocks mismatched root operations: %j', async (patch) => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.ops.set('request-1', { ...h.ops.get('request-1')!, ...patch });
    // Resolve the saved ID, even when the response itself lies about its ID.
    h.api.getOperation.mockImplementation(async () => h.ops.get('request-1')!);
    const resumed = h.restore(); await expect(resumed.resume()).rejects.toThrow(/match|declaration/);
    expect(resumed.state.verified).toBe(false);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it('ignores child terminal events, then verifies root success through Get and fresh resource state', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.api.watch.mockImplementation(async (_filter, event, options) => {
      const root = h.ops.get('request-1')!;
      event({ operation: { ...root, id: 'child', parentId: root.id, status: 'succeeded' }, terminal: true });
      expect(options?.signal?.aborted).toBe(false);
      expect(h.engine.state.verified).toBe(false);
      h.accept();
      event({ operation: { ...root, status: 'succeeded' }, terminal: true });
      expect(options?.signal?.aborted).toBe(true);
      throw new DOMException('aborted', 'AbortError');
    });
    await h.engine.observe();
    expect(h.engine.state.operation?.status).toBe('succeeded');
    expect(h.engine.state.observed?.status?.ready).toBe(true);
    expect(h.api.watch.mock.calls[0][0].includeChildren).toBe(false);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it('does not trust a stream success when authoritative Get still says running', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.api.watch.mockImplementation(async (_filter, event) => {
      event({ operation: { ...h.ops.get('request-1')!, status: 'succeeded' }, terminal: true });
    });
    await expect(h.engine.observe()).rejects.toThrow(/monitoring disconnected/);
    expect(h.engine.state.verified).toBe(false);
    expect(h.engine.state.observed).toBeNull();
  });

  it('does not inherit saved readiness and will not advance when fresh application health is false', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent); h.accept();
    await h.engine.resume();
    expect(h.engine.state.observed?.status?.ready).toBe(true);
    h.live.set('media', { applied: resource, resource: { ...resource, status: { ready: false } } });
    const resumed = h.restore();
    expect(resumed.state.observed).toBeNull();
    await resumed.resume();
    expect(resumed.state.observed?.status?.ready).toBe(false);
    await expect(resumed.next()).rejects.toThrow(/readiness evidence/);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it.each([new ApiError(503, 'provider unavailable'), new UnauthorizedError('expired'), new TypeError('offline')])(
    'does not interpret baseline failure as resource absence: %s', async (error) => {
      const h = harness(); h.engine.save();
      const read = h.api.getResource.getMockImplementation()!;
      h.api.getResource.mockImplementation((av, kind, name) => name === resource.metadata.name ? Promise.reject(error) : read(av, kind, name));
      await expect(h.engine.review()).rejects.toBe(error);
      expect(h.engine.state.preview).toBeNull();
      expect(h.api.preview).not.toHaveBeenCalled();
      expect(h.api.apply).not.toHaveBeenCalled();
    },
  );

  it('invalidates a previously displayed preview when the managed baseline changes before Apply', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.live.set('media', { applied: { ...resource, spec: { unrelated: true } }, resource });
    await expect(h.engine.submit(consent)).rejects.toThrow(/already exists/);
    expect(h.engine.state.preview).toBeNull();
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('requires fresh gates when a new controller preview changes during submission', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.api.preview.mockResolvedValue({ operation: 'create', requiredGates: ['allow_destructive'] });
    await expect(h.engine.submit(consent)).rejects.toThrow(/preview changed/);
    expect(h.api.apply).not.toHaveBeenCalled();
    await h.engine.review();
    await expect(h.engine.submit(consent)).rejects.toThrow(/every required gate/);
  });
});

describe('explicit terminal-failure retry', () => {
  it.each(['failed', 'interrupted', 'canceled'])('allocates a fresh key only after known %s and a new review', async (status) => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.ops.set('request-1', { ...h.ops.get('request-1')!, status });
    const resumed = h.restore(); await resumed.resume();
    await expect(resumed.submit(consent)).rejects.toThrow(/Refresh the preview/);
    await resumed.review();
    await resumed.submit(consent);
    expect(h.api.apply.mock.calls.map(([request]) => request.requestId)).toEqual(['request-1', 'request-2']);
  });

  it('recovers a claimed guest after failed initial Apply without requiring physical absence', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    h.ops.set('request-1', { ...h.ops.get('request-1')!, status: 'failed' });
    await h.engine.review(); await h.engine.submit(consent);
    expect(h.engine.state.operation?.requestId).toBe('request-2');
    expect(h.engine.state.operation?.status).toBe('running');
    expect(h.api.apply.mock.calls[1][0].createOnly).toBe(true);
  });

  it('permits a reviewed partial-resource retry only for the same managed declaration, not a changed or owned baseline', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent); h.accept('request-1', 'failed');
    h.api.preview.mockResolvedValue({ operation: 'create', requiredGates: [] });
    await h.engine.review(); await h.engine.submit(consent);
    expect(h.api.apply.mock.calls[1][0].createOnly).toBe(false);
    h.ops.set('request-2', { ...h.ops.get('request-2')!, status: 'failed' });
    h.live.set('media', { applied: resource, resource: { ...resource, metadata: { ...resource.metadata, ownerRefs: [{ apiVersion: 'other/v1', kind: 'Owner', name: 'owner' }] } } });
    await expect(h.engine.review()).rejects.toThrow(/owned elsewhere/);
    expect(h.api.apply).toHaveBeenCalledTimes(2);
  });

  it('allows observing accepted work while mutation authority is unavailable', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent); h.accept();
    const observationOnly = new DeploymentReview('jellyfin', h.storage, readSavedReview('jellyfin', h.storage, binding)!, { canApply: () => false, api: h.api });
    await observationOnly.resume();
    expect(observationOnly.state.operation?.status).toBe('succeeded');
    await expect(observationOnly.submit(consent)).rejects.toThrow(/does not permit Apply/);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });
});

describe('full guided sequence recovery', () => {
  const inputs: JellyfinInputs = {
    name: 'media', vmName: 'gpu', createVM: true, context: 'pve', node: 'node', templateVMID: 101,
    cpuCores: 4, memoryMiB: 4096, diskGiB: 32,
    storage: 'local-lvm', bridge: 'vmbr0', pciMapping: 'gpu', sshPublicKey: 'ssh-ed25519 public-key', ip: 'dhcp', gateway: '',
    sshUser: 'ubuntu', privateKeyFile: '/keys/id', knownHostsFile: '/keys/known', image: 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
    nasServer: 'nas', nasShare: 'media', nasMediaPath: '', secretProvider: 'file', nasSecret: 'nas', adminUser: 'admin', adminSecret: 'admin',
    viewerUser: 'viewer', viewerSecret: 'viewer', expose: true, hostname: 'media.example.com', tunnel: 'tunnel', accountId: '', zoneId: '',
  };
  async function publicReview() {
    const steps = jellyfinDeployment(inputs);
    const h = harness(steps); h.engine.save();
    for (let i = 0; i < 2; i += 1) {
      await h.engine.review(); await h.engine.submit(consent);
      h.accept(`request-${i + 1}`, 'succeeded', steps[i].resource);
      await h.engine.next();
    }
    return { ...h, app: steps[1].resource };
  }
  it('keeps every VM/app/exposure stage resumable, while refreshing prior operations and readiness before publishing', async () => {
    const steps = jellyfinDeployment(inputs);
    const h = harness(steps); h.engine.save();
    const getResource = h.api.getResource.getMockImplementation()!;
    h.api.getResource.mockImplementation(async (av, kind, name) => {
      const result = await getResource(av, kind, name);
      const serialized = (resource: Resource): Resource => ({ ...resource, metadata: { ...resource.metadata, labels: resource.metadata.labels ?? {}, annotations: resource.metadata.annotations ?? {}, ownerRefs: resource.metadata.ownerRefs ?? [] } });
      return { ...result, resource: serialized(result.resource), applied: result.applied && serialized(result.applied) };
    });
    let engine = h.engine;
    for (let i = 0; i < steps.length; i += 1) {
      await engine.review();
      await engine.submit(consent);
      h.accept(`request-${i + 1}`, 'succeeded', steps[i].resource);
      engine = h.restore();
      expect(engine.state.verified).toBe(false);
      await engine.resume();
      expect(engine.state.operation?.status).toBe('succeeded');
      if (i < steps.length - 1) await engine.next();
    }
    expect(h.api.apply).toHaveBeenCalledTimes(5);
    expect(engine.state.saved.cursor).toBe(4);
    expect(engine.state.observed?.status?.ready).toBe(true);
  });

  it.each([
    { labels: { owner: 'different' } },
    { annotations: { note: 'different' } },
    { ownerRefs: [{ apiVersion: 'apps.openctl.io/v1', kind: 'ComposeApplication', name: 'different' }] },
  ])('keeps changed nonempty prerequisite metadata blocking: %j', async (metadata) => {
    const h = await publicReview();
    const changed = { ...h.app, metadata: { ...h.app.metadata, ...metadata } };
    h.live.set(h.app.metadata.name, { applied: changed, resource: { ...changed, status: { ready: true } } });
    await expect(h.engine.review()).rejects.toThrow();
    expect(h.engine.state.blocker?.code).toBe('baseline_changed');
    expect(h.engine.state.verified).toBe(false);
    expect(h.engine.state.preview).toBeNull();
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.requestId).toHaveBeenCalledTimes(2);
  });

  it.each(unprovenHealth)('cannot advance past a successful application with unproven health: %j', async (status) => {
    const steps = jellyfinDeployment({ ...inputs, createVM: false });
    const h = harness(steps); h.engine.save();
    await h.engine.review(); await h.engine.submit(consent);
    const app = steps[0].resource;
    h.accept('request-1', 'succeeded', app);
    h.live.set(app.metadata.name, { applied: app, resource: { ...app, status } });
    await expect(h.engine.next()).rejects.toThrow(/readiness evidence/);
    expect(h.engine.state.saved.cursor).toBe(0);
    expect(readSavedReview('jellyfin', h.storage, binding)?.cursor).toBe(0);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    h.live.set(app.metadata.name, { applied: app, resource: { ...app, status: {
      ready: true, checks: { gpu: { state: 'ready' }, publicEndpoint: { state: 'not_configured' } },
    } } });
    await h.engine.next();
    expect(h.engine.state.saved.cursor).toBe(1);
    expect(h.engine.state.preview?.operation).toBe('create');
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });

  it.each(unprovenHealth)('blocks final publication admission when an accepted application has unproven health: %j', async (status) => {
    const h = await publicReview();
    h.live.set(h.app.metadata.name, { applied: h.app, resource: { ...h.app, status } });
    await expect(h.engine.submit(consent)).rejects.toThrow();
    expect(h.engine.state.blocker?.code).toBe('readiness_unproven');
    expect(h.engine.state.preview).toBeNull();
    expect(h.engine.state.verified).toBe(false);
    expect(readSavedReview('jellyfin', h.storage, binding)?.steps[2].attempt).toBeUndefined();
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.requestId).toHaveBeenCalledTimes(2);
  });

  it('rechecks earlier application readiness before publishing instead of trusting the saved cursor', async () => {
    const h = await publicReview();
    const app = h.app;
    h.live.set(app.metadata.name, { applied: app, resource: { ...app, spec: { password: 'PRIVATE-OBSERVED-SPEC' }, status: {
      ready: false, message: 'Operator reboot required', credentials: 'PRIVATE-STATUS',
      checks: { gpu: { state: 'not_ready', message: 'Driver unavailable' }, publicEndpoint: { state: 'not_configured' } },
    } } });
    const resumed = h.restore();
    await expect(resumed.resume()).rejects.toThrow();
    await expect(resumed.review()).rejects.toThrow();
    expect(resumed.state.blocker).toEqual({
      target: { apiVersion: app.apiVersion, kind: app.kind, name: app.metadata.name },
      code: 'readiness_unproven',
      message: expect.any(String), detail: 'Operator reboot required',
      checks: [{ name: 'gpu', state: 'not_ready', message: 'Driver unavailable' }, { name: 'publicEndpoint', state: 'not_configured', message: '' }],
    });
    expect(JSON.stringify(resumed.state.blocker)).not.toContain('PRIVATE-');
    expect(JSON.stringify(readSavedReview('jellyfin', h.storage, binding))).not.toContain('Driver unavailable');
    expect(h.restore().state.blocker).toBeNull();
    expect(resumed.state.preview).toBeNull();
    expect(h.api.apply).toHaveBeenCalledTimes(2);
  });

  it('clears stale dependency diagnostics during a recheck and requires fresh consent after repair', async () => {
    const h = await publicReview();
    h.live.set(h.app.metadata.name, { applied: h.app, resource: { ...h.app, status: { ready: false } } });
    await expect(h.engine.review()).rejects.toThrow();
    expect(h.engine.state.blocker?.code).toBe('readiness_unproven');
    const pending = Promise.withResolvers<GetResourceResponse>();
    const original = h.api.getResource.getMockImplementation()!;
    h.api.getResource.mockImplementation((av, kind, name) => name === h.app.metadata.name ? pending.promise : original(av, kind, name));
    const recheck = h.engine.review();
    expect(h.engine.state.blocker).toBeNull();
    expect(h.engine.state.verified).toBe(false);
    pending.resolve({ applied: h.app, resource: { ...h.app, status: { ready: true } } });
    await recheck;
    expect(h.engine.state.blocker).toBeNull();
    expect(h.engine.state.verified).toBe(true);
    expect(h.engine.state.preview?.operation).toBe('create');
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.requestId).toHaveBeenCalledTimes(2);
    await expect(h.engine.submit({ ...consent, publicExposure: false })).rejects.toThrow();
    expect(h.api.apply).toHaveBeenCalledTimes(2);
  });

  it.each(['unavailable', 'different-identity', 'baseline-changed', 'operation-unverified'])('attributes %s to the requested prerequisite without retaining private evidence or resubmitting', async (failure) => {
    const h = await publicReview();
    let target = h.app;
    if (failure === 'unavailable') h.api.getResource.mockRejectedValue(new ApiError(503, 'PRIVATE-ERROR https://user:secret@private.invalid/'));
    if (failure === 'different-identity') h.live.set(h.app.metadata.name, { applied: h.app, resource: { ...h.app, metadata: { name: 'PRIVATE-DIFFERENT-RESOURCE' } } });
    if (failure === 'baseline-changed') h.live.set(h.app.metadata.name, { applied: { ...h.app, spec: { changed: true } }, resource: { ...h.app, status: { message: 'PRIVATE-STATUS', checks: { gpu: { state: 'ready', message: 'PRIVATE-CHECK' } } } } });
    if (failure === 'operation-unverified') {
      const op = h.ops.get('request-2')!;
      h.ops.set('request-2', { ...op, status: 'failed', error: 'PRIVATE-OPERATION-ERROR' });
    }
    // An unreadable first prerequisite is the VM, before the application is read.
    if (failure === 'unavailable') target = h.engine.state.saved.steps[0].resource;
    await expect(h.engine.review()).rejects.toThrow();
    expect(h.engine.state.blocker?.target).toEqual({ apiVersion: target.apiVersion, kind: target.kind, name: target.metadata.name });
    expect(h.engine.state.blocker?.code).toBe(failure === 'operation-unverified' ? 'operation_unverified' : failure === 'baseline-changed' ? 'baseline_changed' : 'observation_unavailable');
    expect(h.engine.state.blocker?.checks).toEqual([]);
    expect(h.engine.state.blocker?.detail).toBe('');
    expect(JSON.stringify(h.engine.state.blocker)).not.toContain('PRIVATE-');
    expect(h.engine.state.preview).toBeNull();
    expect(h.engine.state.verified).toBe(false);
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.requestId).toHaveBeenCalledTimes(2);
  });

  it('can explicitly retry a failed final health stage whose matching declaration was already recorded', async () => {
    const steps = jellyfinDeployment(inputs);
    const h = harness(steps); h.engine.save();
    for (let i = 0; i < steps.length; i += 1) {
      await h.engine.review(); await h.engine.submit(consent);
      h.accept(`request-${i + 1}`, i === steps.length - 1 ? 'failed' : 'succeeded', steps[i].resource);
      if (i < steps.length - 1) await h.engine.next();
    }
    const resumed = h.restore(); await resumed.resume(); await resumed.review();
    await resumed.submit(consent);
    expect(h.api.apply.mock.calls[5][0].requestId).toBe('request-6');
    expect(h.api.apply.mock.calls[5][0].createOnly).toBe(false);
  });

  it('does not inherit public exposure consent after resuming an explicit declaration', async () => {
    const h = harness(jellyfinDeployment(inputs)); h.engine.save();
    const engine = h.restore(); await engine.resume(); await engine.review();
    await expect(engine.submit({ ...consent, publicExposure: false })).rejects.toThrow(/public exposure/);
    expect(h.api.apply).not.toHaveBeenCalled();
  });
});

describe('controller recovery capability and binding', () => {
  it.each([
    { controllerId: 'controller-a', supportsIdempotentApply: false },
    { controllerId: '', supportsIdempotentApply: true },
    {},
  ])('fails closed against an older or identity-less controller: %j', async (controller) => {
    const pingMock = vi.spyOn(ping, 'ping').mockResolvedValue({ echo: '', serverVersion: 'test', ...controller });
    const meMock = vi.spyOn(session, 'whoami').mockResolvedValue({ userId: 'operator-a', principalId: 'operator-a', sessionId: 'session' });
    try { await expect(loadReviewBinding()).rejects.toThrow(/durable request identity/); }
    finally { pingMock.mockRestore(); meMock.mockRestore(); }
  });

  it('invalidates the review if the controller or user changes before Apply', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.api.binding.mockResolvedValue({ ...binding, controllerId: 'controller-b' });
    await expect(h.engine.submit(consent)).rejects.toThrow(/controller or signed-in user changed/);
    expect(h.engine.state.preview).toBeNull();
    expect(h.api.apply).not.toHaveBeenCalled();
  });
});

it('does not echo a server error body that might contain resolved model credentials', () => {
  expect(reviewError(new ApiError(503, 'secret-value-never-display'))).not.toContain('secret-value-never-display');
});


it('does not overwrite a newer browser review when a disposed request receives a late acknowledgement', async () => {
  const h = harness(); h.engine.save(); await h.engine.review();
  const entered = Promise.withResolvers<{ operationId: string; requestId: string | undefined }>();
  const acknowledgement = Promise.withResolvers<{ operationId: string; requestId: string | undefined }>();
  const apply = h.api.apply.getMockImplementation()!;
  h.api.apply.mockImplementation(async (request) => {
    const accepted = await apply(request);
    entered.resolve(accepted);
    return acknowledgement.promise;
  });
  const pending = h.engine.submit(consent);
  const accepted = await entered.promise;
  h.engine.dispose();
  discardSavedReview('jellyfin', h.storage);
  const newer = createSavedReview('jellyfin', binding, [{ ...step, resource: { ...resource, metadata: { ...resource.metadata, name: 'other-media' } } }]);
  const replacement = new DeploymentReview('jellyfin', h.storage, newer, { canApply: () => true, api: h.api });
  replacement.save();
  acknowledgement.resolve(accepted);
  await expect(pending).rejects.toThrow();
  expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].resource.metadata.name).toBe('other-media');
  expect(h.ops.get('request-1')?.id).toBe('op-request-1');
  expect(h.api.apply).toHaveBeenCalledTimes(1);
});

it('binds root and no-auth reviews to the explicit canonical principal instead of the empty root user signal', async () => {
  const pingMock = vi.spyOn(ping, 'ping').mockResolvedValue({ echo: '', serverVersion: 'test', controllerId: binding.controllerId, supportsIdempotentApply: true });
  const meMock = vi.spyOn(session, 'whoami').mockResolvedValue({ userId: '', sessionId: '', principalId: binding.userId, role: 'admin' });
  try { expect(await loadReviewBinding()).toEqual(binding); }
  finally { pingMock.mockRestore(); meMock.mockRestore(); }
});

describe('blocked preview diagnostics', () => {
  it('retains schema errors for inspection without authorizing or persisting them', async () => {
    const h = harness(); h.engine.save();
    const diagnostics = ['spec.target.host: selected guest is unavailable', 'private transient diagnostic'];
    h.api.validate.mockResolvedValueOnce({ errors: diagnostics });
    await expect(h.engine.review()).rejects.toBeInstanceOf(Error);
    expect(h.engine.state.preview?.validationErrors).toEqual(diagnostics);
    expect(h.engine.state.verified).toBe(false);
    await expect(h.engine.submit(consent)).rejects.toBeInstanceOf(Error);
    expect(h.api.preview).not.toHaveBeenCalled();
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(h.storage.getItem(REVIEW_KEYS.jellyfin)).not.toContain('private transient diagnostic');
    expect(h.restore().state.preview).toBeNull();
  });

  it.each([
    { summary: 'unresolved ref', validationErrors: ['target.host: selected VM cannot be read'] },
    { operation: 'create' as const, fieldErrors: [{ path: 'spec.memory.size', message: 'unsupported capacity' }] },
    { summary: 'provider cannot preview this declaration' },
    { operation: 'update' as const, diff: [{ path: 'cpu.cores', desired: '2', observed: '4' }] },
  ])('keeps a failed controller preview visible but never creates an Apply identity: %j', async (preview) => {
    const h = harness(); h.engine.save();
    h.api.preview.mockResolvedValueOnce(preview);
    await expect(h.engine.review()).rejects.toBeInstanceOf(Error);
    expect(h.engine.state.preview).toEqual(preview);
    expect(h.engine.state.verified).toBe(false);
    await expect(h.engine.submit(consent)).rejects.toBeInstanceOf(Error);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt).toBeUndefined();
  });

  it.each([
    { summary: 'unresolved ref', validationErrors: ['target.host: VM disappeared after review'] },
    { operation: 'create' as const, fieldErrors: [{ path: 'spec.nas.share', message: 'invalid share' }] },
  ])('shows new submission-time diagnostics without admitting work: %j', async (preview) => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.api.preview.mockResolvedValueOnce(preview);
    await expect(h.engine.submit(consent)).rejects.toBeInstanceOf(Error);
    expect(h.engine.state.preview).toEqual(preview);
    expect(h.engine.state.verified).toBe(false);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt).toBeUndefined();
  });

  it('replaces transient failure diagnostics only after a fresh valid review', async () => {
    const h = harness(); h.engine.save();
    h.api.preview.mockResolvedValueOnce({ validationErrors: ['old guest outage'] });
    await expect(h.engine.review()).rejects.toBeInstanceOf(Error);
    await h.engine.review();
    expect(h.engine.state.verified).toBe(true);
    expect(h.engine.state.preview?.operation).toBe('create');
    expect(h.engine.state.preview?.validationErrors).toBeUndefined();
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('retains an uncertain original request while displaying a later failed preview', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.api.apply.mockRejectedValueOnce(new TypeError('acknowledgement lost'));
    await expect(h.engine.submit(consent)).rejects.toBeInstanceOf(Error);
    const original = readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt;
    h.api.preview.mockResolvedValueOnce({ operation: 'create', fieldErrors: [{ path: 'spec.target.host', message: 'guest unavailable' }] });
    await expect(h.engine.review()).rejects.toBeInstanceOf(Error);
    expect(h.engine.state.preview?.fieldErrors?.[0].path).toBe('spec.target.host');
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt).toEqual(original);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });
});

describe('referenced guest address preflight', () => {
  const ambiguous = { state: 'running', ipObservation: {
    state: 'unavailable', code: 'guest_ip_ambiguous', message: 'Choose the intended application target explicitly.',
  } };

  it.each([
    ambiguous,
    { state: 'running', ip: '192.0.2.8' },
    { ...guestStatus, state: 'stopped' },
  ])('blocks new application work without trustworthy address evidence: %j', async (status) => {
    const h = harness(); h.engine.save();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status } });
    await expect(h.engine.review()).rejects.toThrow(/guest address evidence/);
    expect(h.engine.state.blocker).toMatchObject({
      target: { apiVersion: VM_API, kind: 'VirtualMachine', name: 'gpu' },
      code: 'address_unavailable', checks: [],
    });
    expect(h.engine.state.preview).toBeNull();
    expect(h.api.preview).not.toHaveBeenCalled();
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(h.restore().state.blocker).toBeNull();
    expect(JSON.stringify(readSavedReview('jellyfin', h.storage, binding))).not.toContain('Choose the intended');
  });

  it('rechecks the selected guest before admission and clears old preview authority on failure', async () => {
    const h = harness(); h.engine.save(); await h.engine.review();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: ambiguous } });
    await expect(h.engine.submit(consent)).rejects.toThrow(/guest address evidence/);
    expect(h.engine.state.blocker?.detail).toContain('guest_ip_ambiguous');
    expect(h.engine.state.preview).toBeNull();
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(readSavedReview('jellyfin', h.storage, binding)?.steps[0].attempt).toBeUndefined();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: guestStatus } });
    await h.engine.review();
    expect(h.engine.state.blocker).toBeNull();
    await expect(h.engine.submit({ ...consent, trustedKey: false })).rejects.toThrow(/verified SSH host key/);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('reuses a freshly verified accepted VM and never resubmits it while repairing address discovery', async () => {
    const h = harness([{ title: 'Provision guest', resource: guest, createOnly: true }, step]);
    h.engine.save(); await h.engine.review(); await h.engine.submit(consent); h.accept('request-1', 'succeeded', guest);
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: ambiguous } });
    await expect(h.engine.next()).rejects.toThrow(/guest address evidence/);
    expect(h.engine.state.saved.cursor).toBe(1);
    expect(h.engine.state.blocker?.target.name).toBe('gpu');
    h.api.getResource.mockClear();
    await expect(h.engine.review()).rejects.toThrow(/guest address evidence/);
    expect(h.api.getResource.mock.calls.filter(([, kind]) => kind === 'VirtualMachine')).toHaveLength(1);
    const accepted = readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt;
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: guestStatus } });
    await h.engine.review();
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(readSavedReview('jellyfin', h.storage, binding)!.steps[0].attempt).toEqual(accepted);
    await h.engine.submit(consent);
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.apply.mock.calls[1][0].resource.kind).toBe('Jellyfin');
  });

  it('still recovers accepted application work read-only when guest address discovery later fails', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent); h.accept();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: ambiguous } });
    const resumed = h.restore(); await resumed.resume();
    expect(resumed.state.operation?.status).toBe('succeeded');
    expect(resumed.state.observed?.status?.ready).toBe(true);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });

  it.each(['unavailable', 'wrong-identity'])('attributes %s safely to the referenced VM', async (failure) => {
    const h = harness(); h.engine.save();
    if (failure === 'unavailable') h.api.getResource.mockRejectedValue(new ApiError(503, 'PRIVATE-TRANSPORT https://token@private.invalid'));
    else h.live.set('gpu', { resource: { ...guest, metadata: { name: 'PRIVATE-WRONG-GUEST' } } });
    await expect(h.engine.review()).rejects.toThrow(/referenced VM/);
    expect(h.engine.state.blocker).toMatchObject({ target: { name: 'gpu' }, code: 'observation_unavailable', detail: '', checks: [] });
    expect(JSON.stringify(h.engine.state.blocker)).not.toContain('PRIVATE-');
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('does not read an unrelated VM for an explicitly declared literal application host', async () => {
    const declared = { ...resource, spec: { ...resource.spec, target: { host: '192.0.2.20' } } };
    const h = harness([{ ...step, resource: declared }]); h.live.delete('gpu'); h.engine.save();
    await h.engine.review();
    expect(h.engine.state.preview?.operation).toBe('create');
    expect(h.api.getResource.mock.calls.some(([, kind]) => kind === 'VirtualMachine')).toBe(false);
  });
});

describe('isolated Compose review profile', () => {
  const compose: DeploymentStep = { ...step, resource: { ...resource, kind: 'ComposeApplication', metadata: { name: 'welcome' } } };
  it('keeps the two fixed profiles in distinct session slots and rejects cross-profile records and sequences', () => {
    const storage = memoryStorage();
    const jellyfin = createSavedReview('jellyfin', binding, [step]);
    const docker = createSavedReview('compose', binding, [compose]);
    new DeploymentReview('jellyfin', storage, jellyfin, { canApply: () => true }).save();
    new DeploymentReview('compose', storage, docker, { canApply: () => true }).save();
    expect(readSavedReview('jellyfin', storage, binding)?.steps[0].resource.kind).toBe('Jellyfin');
    expect(readSavedReview('compose', storage, binding)?.steps[0].resource.kind).toBe('ComposeApplication');
    expect(() => new DeploymentReview('compose', storage, jellyfin, { canApply: () => true })).toThrow(/profile/);
    storage.setItem(REVIEW_KEYS.compose, JSON.stringify(jellyfin));
    expect(() => readSavedReview('compose', storage, binding)).toThrow(/profile/);
    expect(() => createSavedReview('compose', binding, [compose, { ...compose, createOnly: false }])).toThrow(/sequence/);
    expect(() => createSavedReview('jellyfin', binding, [compose])).toThrow(/sequence/);
    discardSavedReview('compose', storage);
    expect(readSavedReview('jellyfin', storage, binding)).toEqual(jellyfin);
  });

  it('validates legacy Jellyfin records only inside their original fixed profile, without inherited consent', () => {
    const storage = memoryStorage();
    const legacy = { ...createSavedReview('jellyfin', binding, [step]) } as Record<string, unknown>;
    delete legacy.profile;
    storage.setItem(REVIEW_KEYS.jellyfin, JSON.stringify(legacy));
    expect(readSavedReview('jellyfin', storage, binding)?.profile).toBe('jellyfin');
    storage.setItem(REVIEW_KEYS.compose, JSON.stringify(legacy));
    expect(() => readSavedReview('compose', storage, binding)).toThrow(/profile/);
  });

  it('reviews VM then app independently, never requiring trust before the new guest exists or replaying accepted VM work', async () => {
    const steps = [{ title: 'Docker guest', resource: guest, createOnly: true }, compose];
    const h = harness(steps, 'compose'); h.engine.save();
    await h.engine.review(); await h.engine.submit({ ...consent, trustedKey: false });
    h.accept('request-1', 'succeeded', guest);
    const resumed = h.restore(); await resumed.resume(); await resumed.next();
    expect(resumed.state.saved.cursor).toBe(1);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    await expect(resumed.submit({ ...consent, trustedKey: false })).rejects.toThrow(/verified SSH host key/);
    await resumed.submit(consent);
    h.accept('request-2', 'succeeded', compose.resource);
    const restored = h.restore(); await restored.resume();
    expect(restored.state.observed?.status?.ready).toBe(true);
    expect(h.api.apply).toHaveBeenCalledTimes(2);
    expect(h.api.apply.mock.calls.map(([request]) => request.resource.kind)).toEqual(['VirtualMachine', 'ComposeApplication']);
    expect(readSavedReview('compose', h.storage, binding)?.steps[1].resource.spec).toEqual(compose.resource.spec);
  });

  it('blocks unknown guest address before review and rechecks the selected address before app admission', async () => {
    const h = harness([compose], 'compose'); h.engine.save();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: { state: 'running', ip: '192.0.2.8' } } });
    await expect(h.engine.review()).rejects.toThrow(/guest address evidence/);
    expect(h.engine.state.blocker?.code).toBe('address_unavailable');
    expect(h.api.preview).not.toHaveBeenCalled();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: guestStatus } });
    await h.engine.review();
    h.live.set('gpu', { applied: guest, resource: { ...guest, status: { ...guestStatus, state: 'stopped' } } });
    await expect(h.engine.submit(consent)).rejects.toThrow(/guest address evidence/);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(readSavedReview('compose', h.storage, binding)?.steps[0].attempt).toBeUndefined();
  });

  it('does not turn accepted operation success into cached application readiness on reload', async () => {
    const h = harness([compose], 'compose'); h.engine.save();
    await h.engine.review(); await h.engine.submit(consent); h.accept('request-1', 'succeeded', compose.resource);
    h.live.set('welcome', { applied: compose.resource, resource: { ...compose.resource, status: { ready: true, checks: { docker: { state: 'unknown' } } } } });
    const resumed = h.restore(); await resumed.resume();
    await expect(resumed.next()).rejects.toThrow(/readiness evidence/);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(resumed.state.preview).toBeNull();
  });

  it('cancels admission when the canonical actor changes during the final preview', async () => {
    const h = harness([compose], 'compose'); h.engine.save(); await h.engine.review();
    h.api.preview.mockImplementationOnce(async () => {
      h.api.binding.mockResolvedValue({ ...binding, userId: 'other-operator' });
      return { operation: 'create', requiredGates: [] };
    });
    await expect(h.engine.submit(consent)).rejects.toThrow(/controller or signed-in user changed/);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(readSavedReview('compose', h.storage, binding)?.steps[0].attempt).toBeUndefined();
  });
});
