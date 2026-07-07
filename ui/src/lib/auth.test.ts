import { describe, it, expect } from 'vitest';
import { get } from 'svelte/store';
import { auth, canMutate } from './auth';

describe('canMutate', () => {
  it('is false only for an explicit viewer', () => {
    auth.set({ kind: 'signed-in', me: { userId: 'v', sessionId: 's', role: 'viewer' } });
    expect(get(canMutate)).toBe(false);
  });

  it('is true for editor and admin', () => {
    auth.set({ kind: 'signed-in', me: { userId: 'e', sessionId: 's', role: 'editor' } });
    expect(get(canMutate)).toBe(true);
    auth.set({ kind: 'signed-in', me: { userId: 'a', sessionId: 's', role: 'admin' } });
    expect(get(canMutate)).toBe(true);
  });

  it('defaults to true when role is missing (older server / --no-auth)', () => {
    auth.set({ kind: 'signed-in', me: { userId: 'x', sessionId: 's' } });
    expect(get(canMutate)).toBe(true);
  });

  it('is true when signed-out / unknown (gate is the server, not the UI)', () => {
    auth.set({ kind: 'signed-out' });
    expect(get(canMutate)).toBe(true);
    auth.set({ kind: 'unknown' });
    expect(get(canMutate)).toBe(true);
  });
});
