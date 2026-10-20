import { afterEach, describe, expect, it, vi } from 'vitest';
import { createCleanupScope } from '../src/core/lifecycle/cleanup-scope';

afterEach(() => vi.useRealTimers());

describe('page cleanup scope', () => {
  it('replaces only the request with the same key and rejects stale results', () => {
    const scope = createCleanupScope();
    const list = scope.latest('list');
    const trace = scope.latest('trace');
    const replacement = scope.latest('list');
    expect(list.signal.aborted).toBe(true);
    expect(list.current()).toBe(false);
    expect(trace.current()).toBe(true);
    list.finish();
    expect(replacement.current()).toBe(true);
    expect(scope.pending('list')).toBe(true);
    scope.dispose();
  });

  it('finishes completed requests without invalidating their replacement', () => {
    const scope = createCleanupScope();
    const done = scope.latest('list');
    done.finish();
    expect(done.current()).toBe(false);
    expect(scope.pending('list')).toBe(false);
    const next = scope.latest('list');
    done.finish();
    expect(next.current()).toBe(true);
    scope.dispose();
  });

  it('can cancel a detail request without affecting a pending list', () => {
    const scope = createCleanupScope();
    const list = scope.latest('list');
    const trace = scope.latest('trace');
    scope.cancel('trace');
    expect(trace.current()).toBe(false);
    expect(trace.signal.aborted).toBe(true);
    expect(list.current()).toBe(true);
    scope.dispose();
  });

  it('cancels all pending work and polling exactly once on leaving a page', () => {
    vi.useFakeTimers();
    const scope = createCleanupScope();
    const list = scope.latest('list');
    const trace = scope.latest('trace');
    const poll = vi.fn();
    scope.interval(poll, 1000);
    vi.advanceTimersByTime(2000);
    expect(poll).toHaveBeenCalledTimes(2);
    scope.dispose();
    scope.dispose();
    vi.advanceTimersByTime(5000);
    expect(poll).toHaveBeenCalledTimes(2);
    expect(list.signal.aborted).toBe(true);
    expect(trace.signal.aborted).toBe(true);
    expect(list.current()).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    expect(scope.latest('late callback').signal.aborted).toBe(true);
    scope.interval(poll, 1000);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('a reentered page cannot revive tokens from a disposed page', () => {
    const oldPage = createCleanupScope();
    const oldRequest = oldPage.latest('list');
    oldPage.dispose();
    const newPage = createCleanupScope();
    expect(newPage.latest('list').current()).toBe(true);
    expect(oldRequest.current()).toBe(false);
    newPage.dispose();
  });
});
