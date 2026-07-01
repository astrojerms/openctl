// TypeScript mirror of internal/schema/form.Field. The controller emits
// this as JSON inside a `string json` proto field; the renderer parses
// once and dispatches on the `type` tag at each node.
//
// Keep in lockstep with internal/schema/form/form.go — any new FieldType
// added there gets a discriminated case here (and a render branch in
// components/FormField.svelte).

export type FieldType =
  | 'string'
  | 'int'
  | 'number'
  | 'bool'
  | 'object'
  | 'array'
  | 'map'
  | 'any'
  | 'unsupported';

export interface FormField {
  name?: string;
  type: FieldType;
  optional?: boolean;
  default?: unknown;
  description?: string;
  const?: unknown;
  min?: number;
  max?: number;
  fields?: FormField[];
  items?: FormField;
  // map: value schema for `{[string]: T}`.
  valueType?: FormField;
  // string + literal disjunction → select options.
  enum?: string[];
  // string + regex constraint → HTML pattern attribute.
  pattern?: string;
  // string + `@options(kind="X" [, apiVersion="Y"])` attribute →
  // dropdown populated from the names of resources of that kind.
  // apiVersion is optional; when absent the UI defaults it to the
  // containing resource's apiVersion.
  optionsSource?: { kind: string; apiVersion?: string };
  // `@oneOf(group="X")` attribute → this field is one alternative in a
  // mutually-exclusive group of siblings. The form renderer groups
  // them into a picker.
  oneOfGroup?: string;
  reason?: string;
}

// pathKey builds a dot-joined path from a name stack. Used both as a
// React-style key for form rows and as the lookup key into the value
// map. Repeating: numeric segments mean array indices (e.g.
// `spec.workers.0.count`).
export function pathKey(path: string[]): string {
  return path.join('.');
}

// initialValue derives a sensible starting value for a Field. Defaults
// override; const wins over default; otherwise:
//   string / int / number → empty
//   bool → false
//   object → recurse into required children only (optional ones stay
//            unset so the form doesn't seed them visually).
//   array → empty list
//   any / unsupported → null
export function initialValue(f: FormField): unknown {
  if (f.const !== undefined) return f.const;
  if (f.default !== undefined) return f.default;
  switch (f.type) {
    case 'string':
      return '';
    case 'int':
    case 'number':
      return null; // null = "unset"; renders as empty input
    case 'bool':
      return false;
    case 'object': {
      const out: Record<string, unknown> = {};
      for (const child of f.fields ?? []) {
        if (!child.optional && child.name) {
          out[child.name] = initialValue(child);
        }
      }
      return out;
    }
    case 'array':
      return [];
    case 'map':
      return {};
    default:
      return null;
  }
}

// fromManifest seeds form state from an existing parsed manifest. Used
// when the user opens the editor on a resource that already exists —
// the form pre-fills with the applied values instead of defaults.
//
// Walks the Field tree and copies matching keys from `data`. Unknown
// keys in `data` are dropped (the user's YAML editor still has them; the
// form just doesn't surface them). Missing required keys fall back to
// initialValue.
export function fromManifest(f: FormField, data: unknown): unknown {
  if (f.const !== undefined) return f.const;
  if (data === undefined || data === null) return initialValue(f);

  switch (f.type) {
    case 'object': {
      const obj = (typeof data === 'object' && !Array.isArray(data))
        ? (data as Record<string, unknown>)
        : {};
      const out: Record<string, unknown> = {};
      for (const child of f.fields ?? []) {
        if (!child.name) continue;
        if (child.name in obj) {
          out[child.name] = fromManifest(child, obj[child.name]);
        } else if (!child.optional) {
          out[child.name] = initialValue(child);
        }
      }
      return out;
    }
    case 'array': {
      const arr = Array.isArray(data) ? data : [];
      if (!f.items) return arr;
      return arr.map((el) => fromManifest(f.items!, el));
    }
    case 'map': {
      const obj = (typeof data === 'object' && !Array.isArray(data))
        ? (data as Record<string, unknown>)
        : {};
      const out: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(obj)) {
        out[k] = f.valueType ? fromManifest(f.valueType, v) : v;
      }
      return out;
    }
    case 'int':
    case 'number':
      return typeof data === 'number' ? data : null;
    case 'bool':
      return typeof data === 'boolean' ? data : false;
    case 'string':
      return typeof data === 'string' ? data : '';
    default:
      return data;
  }
}

// scrubEmpty removes empty optional fields from the generated manifest
// so the YAML preview doesn't show `field: null` everywhere. Required
// fields keep their (possibly empty) value so missing-required errors
// still surface server-side.
//
// Removed entries:
//   - undefined / null (for non-required children)
//   - "" for non-required strings
//   - {} for non-required objects (recursively scrubbed first)
//   - [] for non-required arrays
export function scrubEmpty(f: FormField, value: unknown): unknown {
  switch (f.type) {
    case 'object': {
      if (typeof value !== 'object' || value === null || Array.isArray(value)) {
        return value;
      }
      const src = value as Record<string, unknown>;
      const out: Record<string, unknown> = {};
      for (const child of f.fields ?? []) {
        if (!child.name) continue;
        const present = child.name in src;
        if (!present) {
          if (!child.optional) out[child.name] = initialValue(child);
          continue;
        }
        const scrubbed = scrubEmpty(child, src[child.name]);
        if (child.optional && isEmpty(scrubbed)) continue;
        out[child.name] = scrubbed;
      }
      return out;
    }
    case 'array': {
      if (!Array.isArray(value)) return value;
      if (!f.items) return value;
      return value.map((el) => scrubEmpty(f.items!, el));
    }
    case 'map': {
      // Maps just pass through after dropping empty values when value
      // type is scalar — for `{[string]: string}` we drop entries
      // whose value is the empty string so the preview doesn't carry
      // half-typed rows the user hasn't filled in yet.
      if (typeof value !== 'object' || value === null || Array.isArray(value)) {
        return value;
      }
      const src = value as Record<string, unknown>;
      const out: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(src)) {
        if (k === '' || isEmpty(v)) continue;
        out[k] = f.valueType ? scrubEmpty(f.valueType, v) : v;
      }
      return out;
    }
    default:
      return value;
  }
}

// isEmpty matches the rules scrubEmpty uses to decide what to drop:
// undefined/null, empty strings, empty arrays, and empty objects all
// count as empty. Exported so the form renderer can decide whether to
// show a composite field as a collapsed "+ <name>" affordance or as
// the expanded sub-form.
export function isEmpty(v: unknown): boolean {
  if (v === undefined || v === null) return true;
  if (typeof v === 'string') return v === '';
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === 'object') return Object.keys(v as object).length === 0;
  return false;
}

// extraKeys returns dotted paths in `data` that the schema can't model.
// Used by the Edit pane to decide whether the Form view tab should be
// enabled: if a manifest carries unknown keys, the form would silently
// drop them on save, so we disable the toggle with a tooltip listing
// the offending paths.
//
// Walks object fields by name and array/map values by recursion.
// Schema branches that swallow anything (`any`, `unsupported`,
// `object` with no fields list) short-circuit — those paths can't have
// "extras" because nothing in the schema is more specific.
export function extraKeys(f: FormField, data: unknown, prefix: string[] = []): string[] {
  if (data === undefined || data === null) return [];

  switch (f.type) {
    case 'object': {
      if (typeof data !== 'object' || Array.isArray(data)) return [];
      const obj = data as Record<string, unknown>;
      const known = new Set((f.fields ?? []).map((c) => c.name).filter(Boolean) as string[]);
      const out: string[] = [];
      for (const [k, v] of Object.entries(obj)) {
        if (!known.has(k)) {
          out.push([...prefix, k].join('.'));
          continue;
        }
        const child = (f.fields ?? []).find((c) => c.name === k);
        if (child) out.push(...extraKeys(child, v, [...prefix, k]));
      }
      return out;
    }
    case 'array': {
      if (!Array.isArray(data) || !f.items) return [];
      const out: string[] = [];
      for (let i = 0; i < data.length; i++) {
        out.push(...extraKeys(f.items, data[i], [...prefix, String(i)]));
      }
      return out;
    }
    case 'map': {
      if (typeof data !== 'object' || Array.isArray(data) || !f.valueType) return [];
      const obj = data as Record<string, unknown>;
      const out: string[] = [];
      for (const [k, v] of Object.entries(obj)) {
        out.push(...extraKeys(f.valueType, v, [...prefix, k]));
      }
      return out;
    }
    case 'any':
    case 'unsupported':
      // Schema accepts anything here — no path is "extra".
      return [];
    default:
      return [];
  }
}
