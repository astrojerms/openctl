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

export type Monaco = typeof import('monaco-editor/esm/vs/editor/editor.api');

let cached: Monaco | null = null;

export async function loadMonaco(): Promise<Monaco> {
  if (cached) return cached;
  self.MonacoEnvironment ??= {
    getWorker(_workerId, _label) {
      return new EditorWorker();
    },
  };
  const [api] = await Promise.all([
    import('monaco-editor/esm/vs/editor/editor.api'),
    // Side-effect import that registers the YAML basic-language. Loaded
    // alongside the API so highlighting works on the first render.
    import('monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'),
  ]);
  cached = api;
  return api;
}
