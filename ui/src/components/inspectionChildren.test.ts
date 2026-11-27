import { afterEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import DagView from './DagView.svelte';
import HistoryDiff from './HistoryDiff.svelte';
import { repo, resources, UnauthorizedError, type ChildrenGraphResponse, type ResourceHistory } from '../lib/api';

let component: Record<string, unknown> | undefined;
let target: HTMLDivElement | undefined;
const identity = { apiVersion: 'example.io/v1', kind: 'Saved', resourceName: 'current' };

function host(): HTMLDivElement {
  target = document.createElement('div');
  document.body.append(target);
  return target;
}
async function settle() { await Promise.resolve(); await tick(); }
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target?.remove();
  target = undefined;
  vi.restoreAllMocks();
});

describe('child inspection evidence boundaries', () => {
  it('does not render a late graph after its inspection signal was canceled', async () => {
    const pending = Promise.withResolvers<ChildrenGraphResponse>();
    vi.spyOn(resources, 'childrenGraph').mockReturnValue(pending.promise);
    const controller = new AbortController();
    component = mount(DagView, { target: host(), props: { ...identity, signal: controller.signal } });
    await tick();
    controller.abort();
    pending.resolve({ nodes: [{ id: 'previous', apiVersion: identity.apiVersion, kind: identity.kind, name: 'previous-private-resource', status: 'applied', managed: true, root: true }], edges: [] });
    await settle();
    expect(target?.textContent).not.toContain('previous-private-resource');
  });

  it('does not offer old history after its inspection signal was canceled', async () => {
    const pending = Promise.withResolvers<ResourceHistory>();
    vi.spyOn(repo, 'history').mockReturnValue(pending.promise);
    const controller = new AbortController();
    component = mount(HistoryDiff, { target: host(), props: { ...identity, signal: controller.signal } });
    await tick();
    controller.abort();
    pending.resolve({ commits: [{ sha: 'private-old-revision', subject: 'previous-private-history' }] });
    await settle();
    expect(target?.querySelector('select')).toBeNull();
    expect(target?.textContent).not.toContain('previous-private-history');
  });

  it('ends the parent inspection when a graph read loses authorization', async () => {
    vi.spyOn(resources, 'childrenGraph').mockRejectedValue(new UnauthorizedError('expired'));
    const ended = vi.fn();
    component = mount(DagView, { target: host(), props: { ...identity, onUnauthorized: ended } });
    await settle();
    expect(ended).toHaveBeenCalledOnce();
  });

  it('does not disguise unauthorized history as disabled Git tracking', async () => {
    vi.spyOn(repo, 'history').mockRejectedValue(new UnauthorizedError('expired'));
    const ended = vi.fn();
    component = mount(HistoryDiff, { target: host(), props: { ...identity, onUnauthorized: ended } });
    await settle();
    expect(ended).toHaveBeenCalledOnce();
    expect(target?.querySelector('[role="alert"]')).not.toBeNull();
    expect(target?.querySelector('select')).toBeNull();
  });
});
