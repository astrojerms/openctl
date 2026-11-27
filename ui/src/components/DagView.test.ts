import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import DagView from './DagView.svelte';
import { resources, type ChildrenGraphResponse } from '../lib/api';
import { ops } from '../lib/ops';
import { clearOpsDrawerFocus, opsDrawerFocus } from '../lib/opsDrawer';
import type { OperationRow } from '../lib/watch';

const identity = { apiVersion: 'apps.openctl.io/v1', kind: 'ComposeApplication', resourceName: 'demo' };
const graph: ChildrenGraphResponse = {
  nodes: [
    { id: 'root', apiVersion: identity.apiVersion, kind: identity.kind, name: identity.resourceName, status: 'applied', managed: true, root: true },
    { id: 'child', apiVersion: 'proxmox.openctl.io/v1', kind: 'VM', name: 'demo', status: 'applied', managed: true },
  ],
  edges: [{ from: 'root', to: 'child', relation: 'owns' }],
};
const first = '2026-11-27T10:00:00Z';
const second = '2026-11-27T10:01:00Z';
const third = '2026-11-27T10:02:00Z';
const fourth = '2026-11-27T10:03:00Z';
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;

function operation(id: string, status: string, submittedAt: string, overrides: Partial<OperationRow> = {}): OperationRow {
  return { id, type: 'apply', ...identity, status, submittedAt, ...overrides };
}

async function settle() {
  await Promise.resolve();
  await tick();
  await tick();
}

async function render(rows: OperationRow[]) {
  ops.set(rows);
  component = mount(DagView, { target, props: identity });
  await settle();
}

function root(): SVGGElement {
  return target.querySelector<SVGGElement>('.node.root')!;
}

beforeEach(() => {
  target = document.createElement('div');
  document.body.append(target);
  ops.set([]);
  clearOpsDrawerFocus();
  vi.spyOn(resources, 'childrenGraph').mockResolvedValue(graph);
});

afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  ops.set([]);
  clearOpsDrawerFocus();
  vi.restoreAllMocks();
});

describe('topology operation history', () => {
  it('replaces a stale failure with the latest of three successful requests, regardless of completion or list order', async () => {
    await render([
      operation('rotation', 'succeeded', third),
      operation('first-fail', 'failed', first, { completedAt: '2026-11-27T11:00:00Z' }),
      operation('restart', 'succeeded', fourth),
      operation('recovery', 'succeeded', second),
    ]);

    expect(root().querySelector('.op')?.textContent).toBe('op succeeded');
    expect(root().querySelector('title')?.textContent).toContain('operation succeeded restart');
    expect(root().querySelector('.op-bad')).toBeNull();
    expect(target.textContent).toContain('not resource health');
  });

  it.each(['failed', 'interrupted', 'canceled'])('keeps a genuinely latest %s request visible even when the resource is applied', async (status) => {
    await render([
      operation('current', status, second),
      operation('old-success', 'succeeded', first),
    ]);

    expect(root().classList.contains('good')).toBe(true);
    expect(root().querySelector('.op-bad')?.textContent).toBe(`op ${status}`);
    expect(root().querySelector('title')?.textContent).toContain(`operation ${status} current (not resource health)`);
    root().dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(get(opsDrawerFocus)).toEqual(identity);
    clearOpsDrawerFocus();
    root().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    expect(get(opsDrawerFocus)).toEqual(identity);
  });

  it.each(['pending', 'running'])('preserves %s work alongside terminal history and updates when it succeeds', async (status) => {
    const active = operation('active', status, second);
    const history = [operation('old-fail', 'failed', first), operation('success', 'succeeded', third)];
    await render([...history, active]);

    expect(root().querySelector('.op-warn')?.textContent).toBe(`op ${status}`);
    expect(root().querySelector('title')?.textContent).toContain(`operation ${status} active`);
    ops.set([...history, { ...active, status: 'succeeded', completedAt: fourth }]);
    await settle();
    expect(root().querySelector('.op')?.textContent).toBe('op succeeded');
    expect(root().querySelector('title')?.textContent).toContain('operation succeeded success');
  });

  it('uses the latest active submission rather than the latest start time', async () => {
    await render([
      operation('new-pending', 'pending', second),
      operation('old-running', 'running', first, { startedAt: third }),
    ]);
    expect(root().querySelector('title')?.textContent).toContain('operation pending new-pending');
  });

  it('indexes nested operations by fully-qualified identity without mixing resource kinds or API versions', async () => {
    const child = { apiVersion: 'proxmox.openctl.io/v1', kind: 'VM', resourceName: 'demo', parentId: 'parent' };
    await render([
      operation('parent', 'succeeded', third, {
        children: [
          operation('child-fail', 'failed', first, child),
          operation('child-good', 'succeeded', second, child),
        ],
      }),
      operation('other-api', 'running', fourth, { apiVersion: 'other.io/v1' }),
      operation('other-kind', 'failed', fourth, { kind: 'OtherApplication' }),
      operation('other-name', 'failed', fourth, { resourceName: 'other' }),
      operation('incomplete', 'running', fourth, { apiVersion: undefined }),
    ]);

    expect(root().querySelector('title')?.textContent).toContain('operation succeeded parent');
    const childNode = target.querySelector<SVGGElement>('.node:not(.root)')!;
    expect(childNode.querySelector('title')?.textContent).toContain('operation succeeded child-good');
    childNode.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }));
    expect(get(opsDrawerFocus)).toEqual({ apiVersion: child.apiVersion, kind: child.kind, resourceName: child.resourceName });
  });

  it.each(['startedAt', 'completedAt'] as const)('falls back to %s when submission time is unavailable', async (field) => {
    await render([
      operation('old-fail', 'failed', first),
      operation('recovered', 'succeeded', '', { [field]: second }),
    ]);
    expect(root().querySelector('title')?.textContent).toContain('operation succeeded recovered');
  });

  it('uses operation identity to keep equal-timestamp selection stable across store ordering', async () => {
    const rows = [operation('b-success', 'succeeded', second), operation('a-failed', 'failed', second)];
    await render(rows);
    expect(root().querySelector('title')?.textContent).toContain('operation succeeded b-success');
    ops.set([...rows].reverse());
    await settle();
    expect(root().querySelector('title')?.textContent).toContain('operation succeeded b-success');
  });
});
