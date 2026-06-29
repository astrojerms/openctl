// Monaco bootstrap. We dynamic-import everything so Vite emits the
// editor + worker as their own chunks that the list/detail/home pages
// don't fetch. The first visit to /edit takes the network hit (~1 MB
// gzipped, browser-cached after); subsequent navigation is instant.
//
// We only pull the basic YAML language (syntax highlighting); validation
// comes from the server-side SchemaService.Validate call surfaced as
// Monaco markers. Other languages (json, css, html, ts) are not loaded;
// Monaco falls back to plaintext for them, which is fine — we only ever
// open YAML in this editor.

import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';

declare global {
  interface Window {
    MonacoEnvironment?: {
      getWorker: (workerId: string, label: string) => Worker;
    };
  }
}

// The runtime import targets `editor.api` (skips `editor.main`'s
// auto-load-every-language barrel), but TypeScript resolves types
// through the package's `.` export entry which only declares
// `editor.main.d.ts`. The Monaco API surface is identical between the
// two — `editor.api` is the subset re-exported from `editor.main` —
// so we use the bare `monaco-editor` import for types only.
export type Monaco = typeof import('monaco-editor');

let cached: Monaco | null = null;

export async function loadMonaco(): Promise<Monaco> {
  if (cached) return cached;
  self.MonacoEnvironment ??= {
    getWorker(_workerId, _label) {
      return new EditorWorker();
    },
  };
  const [api] = await Promise.all([
    // @ts-expect-error — subpath export carries no types; surface is
    // identical to the bare `monaco-editor` types declared above.
    import('monaco-editor/esm/vs/editor/editor.api'),
    // Side-effect import that registers the YAML basic-language. Loaded
    // alongside the API so highlighting works on the first render.
    // @ts-expect-error — subpath export carries no types; runtime only.
    import('monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'),
  ]);
  cached = api as Monaco;
  return cached;
}
