import { derived, writable, type Readable, type Writable } from 'svelte/store';
import { session, UnauthorizedError, type WhoAmIResponse } from './api';

export type AuthState =
  | { kind: 'unknown' }       // initial — bootstrapping check pending
  | { kind: 'signed-out' }
  | { kind: 'signed-in'; me: WhoAmIResponse };

export const auth: Writable<AuthState> = writable({ kind: 'unknown' });

// canMutate is false only for an explicit `viewer` — every other role (editor,
// admin) and the unknown/empty case (older server, --no-auth) allows mutation.
// This is a UX gate that mirrors server-side RBAC (mutations need editor+); the
// server remains the real enforcement point, so defaulting unknown to "allow"
// never grants access the server would deny.
export const canMutate: Readable<boolean> = derived(auth, ($auth) =>
  $auth.kind === 'signed-in' ? $auth.me.role !== 'viewer' : true,
);

// refresh probes /v1/session/whoami to decide whether the existing cookie
// is valid. Used on first load and after a successful Login. Errors that
// aren't 401 are logged but treated as "signed-out" — the user can always
// re-login, and we don't want a transient API blip to lock them out.
export async function refresh(): Promise<void> {
  try {
    const me = await session.whoami();
    auth.set({ kind: 'signed-in', me });
  } catch (err) {
    if (!(err instanceof UnauthorizedError)) {
      console.warn('whoami failed:', err);
    }
    auth.set({ kind: 'signed-out' });
  }
}

export async function login(rootToken: string, displayName: string): Promise<void> {
  await session.login(rootToken, displayName);
  // Cookie is set by the gateway response; whoami confirms it round-trips.
  await refresh();
}

export async function logout(): Promise<void> {
  try {
    await session.logout();
  } catch (err) {
    // Even if logout RPC fails, clear local state — the cookie may still
    // be set, but the user explicitly asked to be signed out.
    console.warn('logout RPC failed:', err);
  }
  auth.set({ kind: 'signed-out' });
}
