import { describe, expect, it, vi } from 'vitest';
import { ApiError, ping, session, UnauthorizedError, type ApplyRequest, type GetResourceResponse, type Operation, type Resource } from './api';
import { APP_API, jellyfinDeployment, type DeploymentStep, type JellyfinInputs } from './jellyfin';
import { createSavedReview, DeploymentReview, discardSavedReview, loadReviewBinding, MAX_REVIEW_BYTES, readSavedReview, REVIEW_KEY, reviewError, type ReviewAPI, type ReviewConsent } from './deploymentReview';

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
function memoryStorage() {
  const data = new Map<string, string>();
  return {
    getItem: vi.fn((key: string) => data.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => { data.set(key, value); }),
    removeItem: vi.fn((key: string) => { data.delete(key); }),
  };
}
function harness(steps: DeploymentStep[] = [step]) {
  const storage = memoryStorage();
  const ops = new Map<string, Operation>();
  const live = new Map<string, GetResourceResponse>();
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
  const engine = new DeploymentReview(storage, createSavedReview(binding, steps), { canApply: () => true, api });
  const restore = () => new DeploymentReview(storage, readSavedReview(storage, binding)!, { canApply: () => true, api });
  const accept = (key = 'request-1', status = 'succeeded', intent = resource) => {
    const op = ops.get(key)!;
    ops.set(key, { ...op, status });
    live.set(intent.metadata.name, { applied: intent, resource: { ...intent, status: { ready: true } } });
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
    const saved = readSavedReview(h.storage, binding)!;
    expect(saved.steps[0].resource).toEqual(resource);
    expect(Object.keys(saved)).toEqual(['version', 'binding', 'cursor', 'steps']);
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
    expect(() => readSavedReview(h.storage, other)).toThrow(/different controller or signed-in user/);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it.each(['not-json', 'null', '{}', JSON.stringify({ version: 0, binding, cursor: 0, steps: [step] }), ' '.repeat(MAX_REVIEW_BYTES + 1)])(
    'fails closed for invalid/versioned/oversized saved data', (raw) => {
      const h = harness(); h.storage.setItem(REVIEW_KEY, raw);
      expect(() => readSavedReview(h.storage, binding)).toThrow();
      expect(h.api.apply).not.toHaveBeenCalled();
    },
  );

  it('rejects cached success or inherited consent fields instead of treating them as evidence', () => {
    const h = harness();
    h.storage.setItem(REVIEW_KEY, JSON.stringify({ ...createSavedReview(binding, [step]), ready: true, publicConsent: true }));
    expect(() => readSavedReview(h.storage, binding)).toThrow(/malformed/);
  });

  it('removes inherited request-consent annotations without losing desired metadata or secret markers', () => {
    const declared = { ...resource, metadata: { ...resource.metadata, annotations: { note: 'keep', 'openctl.io/allow-destructive': 'true' } } };
    const saved = createSavedReview(binding, [{ ...step, resource: declared }]);
    expect(saved.steps[0].resource.metadata.annotations).toEqual({ note: 'keep' });
    expect(saved.steps[0].resource.spec).toEqual(resource.spec);
    expect(declared.metadata.annotations['openctl.io/allow-destructive']).toBe('true');
  });

  it('refuses expanded credentials and private-key bytes in browser recovery records', () => {
    expect(() => createSavedReview(binding, [{ ...step, resource: { ...resource, spec: { password: 'plaintext' } } }])).toThrow(/secret references/);
    expect(() => createSavedReview(binding, [{ ...step, resource: { ...resource, spec: { target: '-----BEGIN OPENSSH PRIVATE KEY-----' } } }])).toThrow(/credential material/);
  });

  it('discards only browser state without canceling operations or touching resources', async () => {
    const h = harness(); h.engine.save(); await h.engine.review(); await h.engine.submit(consent);
    const operationCount = h.ops.size;
    discardSavedReview(h.storage);
    expect(readSavedReview(h.storage, binding)).toBeNull();
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
    expect(readSavedReview(h.storage, binding)!.steps[0].attempt?.operationId).toBe('op-request-1');
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
    const original = readSavedReview(h.storage, binding)!.steps[0].attempt!.requestId;
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
      expect(readSavedReview(h.storage, binding)!.steps[0].attempt?.requestId).toBe(request.requestId);
      return apply(request);
    });
    await h.engine.submit(consent);
    h.api.watch.mockImplementation(async () => {
      expect(readSavedReview(h.storage, binding)!.steps[0].attempt?.operationId).toBe('op-request-1');
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
    expect(readSavedReview(h.storage, binding)!.steps[0].attempt?.requestId).toBe('request-1');
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
      const h = harness(); h.engine.save(); h.api.getResource.mockRejectedValue(error);
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
    const apply = h.api.apply.getMockImplementation()!;
    h.api.apply.mockImplementation(async (request) => {
      if (request.createOnly) throw new ApiError(409, 'the original failed Apply already claimed this guest');
      return apply(request);
    });
    await h.engine.review(); await h.engine.submit(consent);
    expect(h.engine.state.operation?.requestId).toBe('request-2');
    expect(h.engine.state.operation?.status).toBe('running');
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
    const observationOnly = new DeploymentReview(h.storage, readSavedReview(h.storage, binding)!, { canApply: () => false, api: h.api });
    await observationOnly.resume();
    expect(observationOnly.state.operation?.status).toBe('succeeded');
    await expect(observationOnly.submit(consent)).rejects.toThrow(/does not permit Apply/);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
  });
});

describe('full guided sequence recovery', () => {
  const inputs: JellyfinInputs = {
    name: 'media', vmName: 'gpu', createVM: true, context: 'pve', node: 'node', templateVMID: 101,
    storage: 'local-lvm', bridge: 'vmbr0', pciMapping: 'gpu', sshPublicKey: 'ssh-ed25519 public-key', ip: 'dhcp', gateway: '',
    sshUser: 'ubuntu', privateKeyFile: '/keys/id', knownHostsFile: '/keys/known', image: 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
    nasServer: 'nas', nasShare: 'media', nasMediaPath: '', secretProvider: 'file', nasSecret: 'nas', adminUser: 'admin', adminSecret: 'admin',
    viewerUser: 'viewer', viewerSecret: 'viewer', expose: true, hostname: 'media.example.com', tunnel: 'tunnel', accountId: '', zoneId: '',
  };
  it('keeps every VM/app/exposure stage resumable, while refreshing prior operations and readiness before publishing', async () => {
    const steps = jellyfinDeployment(inputs);
    const h = harness(steps); h.engine.save();
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

  it('rechecks earlier application readiness before publishing instead of trusting the saved cursor', async () => {
    const steps = jellyfinDeployment(inputs);
    const h = harness(steps); h.engine.save();
    for (let i = 0; i < 2; i += 1) {
      await h.engine.review(); await h.engine.submit(consent);
      h.accept(`request-${i + 1}`, 'succeeded', steps[i].resource);
      await h.engine.next();
    }
    const app = steps[1].resource;
    h.live.set(app.metadata.name, { applied: app, resource: { ...app, status: { ready: false } } });
    const resumed = h.restore();
    await expect(resumed.resume()).rejects.toThrow(/not currently ready/);
    await expect(resumed.review()).rejects.toThrow(/not currently ready/);
    expect(resumed.state.preview).toBeNull();
    expect(h.api.apply).toHaveBeenCalledTimes(2);
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
  discardSavedReview(h.storage);
  const newer = createSavedReview(binding, [{ ...step, resource: { ...resource, metadata: { ...resource.metadata, name: 'other-media' } } }]);
  const replacement = new DeploymentReview(h.storage, newer, { canApply: () => true, api: h.api });
  replacement.save();
  acknowledgement.resolve(accepted);
  await expect(pending).rejects.toThrow();
  expect(readSavedReview(h.storage, binding)!.steps[0].resource.metadata.name).toBe('other-media');
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
    expect(h.storage.getItem(REVIEW_KEY)).not.toContain('private transient diagnostic');
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
    expect(readSavedReview(h.storage, binding)!.steps[0].attempt).toBeUndefined();
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
    expect(readSavedReview(h.storage, binding)!.steps[0].attempt).toBeUndefined();
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
    const original = readSavedReview(h.storage, binding)!.steps[0].attempt;
    h.api.preview.mockResolvedValueOnce({ operation: 'create', fieldErrors: [{ path: 'spec.target.host', message: 'guest unavailable' }] });
    await expect(h.engine.review()).rejects.toBeInstanceOf(Error);
    expect(h.engine.state.preview?.fieldErrors?.[0].path).toBe('spec.target.host');
    expect(readSavedReview(h.storage, binding)!.steps[0].attempt).toEqual(original);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });
});
