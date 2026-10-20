import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAPI } from '../src/core/api/client';

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const setup = (fetch = vi.fn<typeof globalThis.fetch>()) => {
  const onUnauthorized = vi.fn();
  return { fetch, onUnauthorized, api: createAPI({ fetch, onUnauthorized }) };
};
const untilAborted = (_path: RequestInfo | URL, options?: RequestInit): Promise<Response> =>
  new Promise((_resolve, reject) => options?.signal?.addEventListener('abort',
    () => reject(new DOMException('aborted', 'AbortError')), { once: true }));

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

describe('API transport contract', () => {
  it.each([null, false, 0, [], { new_field: '<untrusted>' }])('returns valid JSON unchanged: %j', async value => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(json(value));
    expect(await api('/api/state')).toEqual(value);
    expect(fetch).toHaveBeenCalledWith('/api/state', expect.objectContaining({
      method: 'GET', credentials: 'same-origin', headers: {}, body: undefined,
    }));
  });

  it.each([null, false, 0, { enabled: true }])('preserves POST JSON semantics for %j', async body => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(json({ saved: true }));
    await api('/api/settings', body);
    expect(fetch).toHaveBeenCalledWith('/api/settings', expect.objectContaining({
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    }));
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('preserves a backend error message and status without retrying writes', async () => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(json({ error: '凭据失效' }, 403));
    await expect(api('/api/jobs', {})).rejects.toMatchObject({ name: 'ApiError', code: 'HTTP', status: 403, message: '凭据失效' });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each(['<html>private proxy diagnostic</html>', '', 'not JSON'])('handles non-JSON HTTP errors safely', async body => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(new Response(body, { status: 502 }));
    await expect(api('/api/state')).rejects.toMatchObject({ code: 'HTTP', status: 502, message: '请求失败（HTTP 502），请重试' });
  });

  it.each([{}, null, [], { error: { secret: 'unexpected object' } }])('handles a missing or invalid error field', async value => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(json(value, 500));
    await expect(api('/api/state')).rejects.toMatchObject({ code: 'HTTP', status: 500, message: '请求失败（HTTP 500），请重试' });
  });

  it.each(['<html>please login</html>', JSON.stringify({ error: '会话失效' })])('handles 401 before decoding its body', async body => {
    const { api, fetch, onUnauthorized } = setup();
    fetch.mockResolvedValue(new Response(body, { status: 401 }));
    await expect(api('/api/state')).rejects.toMatchObject({ code: 'HTTP', status: 401 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    fetch.mockResolvedValue(new Response(body, { status: 401 }));
    await expect(api('/api/login', { password: 'fixture' })).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it.each(['<html>unexpected success page</html>', ''])('reports invalid success JSON without exposing its body', async body => {
    const { api, fetch } = setup();
    fetch.mockResolvedValue(new Response(body));
    await expect(api('/api/state')).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200, message: '服务返回了无效的 JSON 数据，请重试' });
  });

  it('reports network failure with no automatic retries', async () => {
    const { api, fetch } = setup();
    fetch.mockRejectedValue(new TypeError('private network details'));
    await expect(api('/api/state')).rejects.toMatchObject({ code: 'NETWORK', message: '网络连接失败，请检查连接后重试' });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe('API cancellation, deadlines and resource cleanup', () => {
  it('does not send an already cancelled request', async () => {
    const { api, fetch, onUnauthorized } = setup();
    const controller = new AbortController();
    controller.abort();
    await expect(api('/api/state', undefined, { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' });
    expect(fetch).not.toHaveBeenCalled();
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it('keeps user cancellation distinct from network or timeout failure', async () => {
    const { api, fetch } = setup();
    fetch.mockImplementation(untilAborted);
    const controller = new AbortController();
    const result = expect(api('/api/state', undefined, { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' });
    controller.abort();
    await result;
  });

  it('does not let a stale unauthorized response log out the current page', async () => {
    const { api, fetch, onUnauthorized } = setup();
    let complete!: (response: Response) => void;
    fetch.mockImplementation(() => new Promise(resolve => { complete = resolve; }));
    const controller = new AbortController();
    const result = expect(api('/api/state', undefined, { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' });
    controller.abort();
    complete(new Response('unauthorized', { status: 401 }));
    await result;
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it('aborts a stalled GET at its default 60-second deadline', async () => {
    vi.useFakeTimers();
    const { api, fetch } = setup();
    fetch.mockImplementation(untilAborted);
    const result = expect(api('/api/state')).rejects.toMatchObject({ code: 'TIMEOUT', message: '请求超时，请重试' });
    await vi.advanceTimersByTimeAsync(59_999);
    expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await result;
    expect(vi.getTimerCount()).toBe(0);
  });

  it('applies the deadline while reading a response body too', async () => {
    vi.useFakeTimers();
    const { api, fetch } = setup();
    fetch.mockImplementation(async (_path, options) => ({
      ok: true, status: 200,
      json: () => new Promise((_resolve, reject) => options?.signal?.addEventListener('abort',
        () => reject(new DOMException('body aborted', 'AbortError')), { once: true })),
    }) as Response);
    const result = expect(api('/api/state', undefined, { timeoutMs: 10 })).rejects.toMatchObject({ code: 'TIMEOUT' });
    await vi.advanceTimersByTimeAsync(10);
    await result;
  });

  it.each([['GET with explicit opt-out', undefined, 0], ['POST without a deadline', {}, undefined]] as const)(
    'does not impose a deadline on %s', async (_label, body, timeoutMs) => {
      vi.useFakeTimers();
      const { api, fetch } = setup();
      let complete!: (response: Response) => void;
      fetch.mockImplementation(() => new Promise(resolve => { complete = resolve; }));
      const result = api('/api/jobs', body, { timeoutMs });
      await vi.advanceTimersByTimeAsync(120_000);
      expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(false);
      complete(json({ done: true }));
      expect(await result).toEqual({ done: true });
    },
  );

  it('allows a caller to explicitly time out a write without retrying it', async () => {
    vi.useFakeTimers();
    const { api, fetch } = setup();
    fetch.mockImplementation(untilAborted);
    const result = expect(api('/api/jobs', {}, { timeoutMs: 10 })).rejects.toMatchObject({ code: 'TIMEOUT' });
    await vi.advanceTimersByTimeAsync(10);
    await result;
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each([true, false])('removes its external listener and timer on completion (success=%s)', async success => {
    vi.useFakeTimers();
    const { api, fetch } = setup();
    fetch.mockResolvedValue(json({}, success ? 200 : 500));
    const controller = new AbortController();
    const add = vi.spyOn(controller.signal, 'addEventListener');
    const remove = vi.spyOn(controller.signal, 'removeEventListener');
    if (success) await api('/api/state', undefined, { signal: controller.signal });
    else await expect(api('/api/state', undefined, { signal: controller.signal })).rejects.toMatchObject({ code: 'HTTP' });
    expect(remove).toHaveBeenCalledWith('abort', add.mock.calls[0][1]);
    expect(vi.getTimerCount()).toBe(0);
  });
});
