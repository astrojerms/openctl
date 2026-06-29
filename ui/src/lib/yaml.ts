// Thin wrapper around the `yaml` package so the rest of the app uses a
// single import path and consistent options. The Resource proto field
// order isn't guaranteed across the wire; serialising via `yaml.stringify`
// on the JS object preserves insertion order of the keys we choose to
// emit, which is what the editor wants.

import * as YAML from 'yaml';
import type { Resource } from './api';

// Stable top-level key order matches what `openctl ctl get -o yaml` would
// produce — apiVersion, kind, metadata, spec, status. Lets users compare
// editor output against CLI output without thinking about ordering.
const STRINGIFY_OPTS: YAML.ToStringOptions = {
  indent: 2,
  defaultStringType: 'PLAIN',
  lineWidth: 100,
};

export interface ManifestLike {
  apiVersion: string;
  kind: string;
  metadata?: Record<string, unknown>;
  spec?: unknown;
}

// resourceToYAML serialises a Resource (or partial manifest) into the
// canonical YAML shape, omitting fields the editor shouldn't edit
// (status is observed-only; drift is a derived computation).
export function resourceToYAML(r: Resource | ManifestLike): string {
  const out: ManifestLike = {
    apiVersion: r.apiVersion,
    kind: r.kind,
  };
  if (r.metadata) out.metadata = r.metadata as Record<string, unknown>;
  if (r.spec !== undefined) out.spec = r.spec;
  return YAML.stringify(out, STRINGIFY_OPTS);
}

export interface ParsedManifest {
  apiVersion?: string;
  kind?: string;
  metadata?: { name?: string; labels?: Record<string, string>; annotations?: Record<string, string> };
  spec?: unknown;
}

// parseYAML returns the parsed document plus any parse error. Throws
// nothing; callers render the error inline. Empty/blank text returns an
// empty object so the editor doesn't error before the user has typed
// anything.
export function parseYAML(text: string): { doc: ParsedManifest; error: string } {
  const trimmed = text.trim();
  if (!trimmed) return { doc: {}, error: '' };
  try {
    const v = YAML.parse(text);
    if (v === null || typeof v !== 'object' || Array.isArray(v)) {
      return { doc: {}, error: 'top-level value must be a mapping' };
    }
    return { doc: v as ParsedManifest, error: '' };
  } catch (err) {
    return { doc: {}, error: err instanceof Error ? err.message : String(err) };
  }
}
