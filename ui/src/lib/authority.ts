import { derived, writable } from 'svelte/store';
import { controllerConfigApi } from './api';
import { auth, canMutate } from './auth';

export type ManifestMode = 'mirror' | 'gitops';
export const manifestAuthority = writable<{ mode: ManifestMode | null; loading: boolean; error: string }>({ mode: null, loading: true, error: '' });
let revision = 0;

export function resetAuthority(): void {
  revision += 1;
  manifestAuthority.set({ mode: null, loading: true, error: '' });
}

// Repo status also carries running mode, allowing its existing poll to observe
// a restarted controller without adding another periodic request.
export function recordAuthority(mode: unknown): void {
  if (mode !== 'mirror' && mode !== 'gitops') return;
  revision += 1;
  manifestAuthority.set({ mode, loading: false, error: '' });
}

export async function refreshAuthority(): Promise<void> {
  const requestRevision = ++revision;
  manifestAuthority.set({ mode: null, loading: true, error: '' });
  try {
    const response = await controllerConfigApi.get();
    if (requestRevision !== revision) return;
    if (response.manifestMode !== 'mirror' && response.manifestMode !== 'gitops') {
      throw new Error('The controller did not report its running manifest authority.');
    }
    manifestAuthority.set({ mode: response.manifestMode, loading: false, error: '' });
  } catch (err) {
    if (requestRevision !== revision) return;
    manifestAuthority.set({ mode: null, loading: false, error: err instanceof Error ? err.message : String(err) });
  }
}

export const canEditDesired = derived([canMutate, manifestAuthority], ([$canMutate, authority]) => $canMutate && authority.mode === 'mirror');
export const canAdminister = derived(auth, (state) => state.kind === 'signed-in' && (!state.me.role || state.me.role === 'admin'));
export const canManageOwnership = derived([canAdminister, manifestAuthority], ([admin, authority]) => admin && authority.mode === 'mirror');
export const desiredMutationHint = derived([canMutate, manifestAuthority], ([$canMutate, authority]) => {
  if (!$canMutate) return 'Read-only session — requires editor or admin role';
  if (authority.mode === 'gitops') return 'GitOps owns desired intent. Edit the upstream repository instead of applying or deleting here.';
  if (authority.loading) return 'Loading the running controller manifest authority…';
  if (!authority.mode) return 'Cannot verify manifest authority. Refresh controller authority before changing desired intent.';
  return '';
});
