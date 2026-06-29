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
    default:
      return value;
  }
}

function isEmpty(v: unknown): boolean {
  if (v === undefined || v === null) return true;
  if (typeof v === 'string') return v === '';
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === 'object') return Object.keys(v as object).length === 0;
  return false;
}
