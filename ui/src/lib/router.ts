import { writable, type Writable } from 'svelte/store';

// Tiny hash-based router. Avoids pulling in svelte-routing/tinro for the
// dozen routes the U3.x UI needs. Patterns recognised:
//
//   #/                                       → { name: 'home' }
//   #/k/<apiVersion>/<kind>                  → { name: 'list', apiVersion, kind }
//   #/k/<apiVersion>/<kind>/new              → { name: 'create', apiVersion, kind }
//   #/k/<apiVersion>/<kind>/<resourceName>   → { name: 'detail', apiVersion, kind, resourceName }
//   #/k/<apiVersion>/<kind>/<resourceName>/edit → { name: 'edit', ... }
//
// apiVersion contains a slash ("k3s.openctl.io/v1") which is itself
// significant in the path, so we URL-encode each segment when constructing
// links via `routeHref` and split on `/` carefully when parsing — the
// encoding round-trips through location.hash so links survive copy/paste.
//
// `new` is reserved as a resourceName: a resource literally named "new"
// would route to create instead of detail. The Proxmox/k3s schemas pin
// names to a kebab-case regex that doesn't permit a bare "new" today,
// but if that ever changes we'd encode the literal collision separately.

export type Route =
  | { name: 'home' }
  | { name: 'list'; apiVersion: string; kind: string }
  | { name: 'create'; apiVersion: string; kind: string }
  | { name: 'detail'; apiVersion: string; kind: string; resourceName: string }
  | { name: 'edit'; apiVersion: string; kind: string; resourceName: string };

function parse(hash: string): Route {
  const path = hash.replace(/^#/, '').replace(/^\//, '');
  if (!path) return { name: 'home' };

  const parts = path.split('/').map(decodeURIComponent);
  // ['k', '<encoded-apiVersion-half-1>', '<encoded-apiVersion-half-2>', '<kind>', '<name>?', 'edit'?]
  if (parts[0] === 'k' && parts.length >= 4) {
    // apiVersion is encoded as two segments because it has a single slash.
    const apiVersion = `${parts[1]}/${parts[2]}`;
    const kind = parts[3];
    if (parts.length === 4) return { name: 'list', apiVersion, kind };
    if (parts.length === 5) {
      if (parts[4] === 'new') return { name: 'create', apiVersion, kind };
      return { name: 'detail', apiVersion, kind, resourceName: parts[4] };
    }
    if (parts.length === 6 && parts[5] === 'edit') {
      return { name: 'edit', apiVersion, kind, resourceName: parts[4] };
    }
  }
  return { name: 'home' };
}

export function routeHref(route: Route): string {
  if (route.name === 'home') return '#/';
  const [g, v] = route.apiVersion.split('/');
  const base = `#/k/${encodeURIComponent(g)}/${encodeURIComponent(v)}/${encodeURIComponent(route.kind)}`;
  if (route.name === 'list') return base;
  if (route.name === 'create') return `${base}/new`;
  const detail = `${base}/${encodeURIComponent(route.resourceName)}`;
  if (route.name === 'detail') return detail;
  // edit
  return `${detail}/edit`;
}

export const route: Writable<Route> = writable(parse(location.hash));

window.addEventListener('hashchange', () => route.set(parse(location.hash)));

export function navigate(to: Route): void {
  location.hash = routeHref(to);
}
