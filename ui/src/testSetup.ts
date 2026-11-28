import { Blob as NodeBlob } from 'node:buffer';
import { URL as NodeURL } from 'node:url';

// jsdom implements option selection but lacks modern Blob APIs. Use the real
// Node implementations; download tests explicitly spy on URL creation.
Object.defineProperty(globalThis, 'Blob', { configurable: true, writable: true, value: NodeBlob });
Object.defineProperties(URL, {
  createObjectURL: { configurable: true, writable: true, value: NodeURL.createObjectURL },
  revokeObjectURL: { configurable: true, writable: true, value: NodeURL.revokeObjectURL },
});
