export interface ApiOptions {
  signal?: AbortSignal;
  /** 0 disables the deadline. Writes have no default deadline or retries. */
  timeoutMs?: number;
}

export function createAPI(dependencies: {
  fetch: typeof fetch;
  onUnauthorized: () => void;
  readTimeoutMs?: number;
}) {
  const error = (code: string, message: string, status?: number) =>
    Object.assign(new Error(message), { name: 'ApiError', code, status });

  return async function request(path: string, body?: unknown, options: ApiOptions = {}): Promise<unknown> {
    const controller = new AbortController();
    const timeoutMs = options.timeoutMs ?? (body === undefined ? dependencies.readTimeoutMs ?? 60_000 : 0);
    let timedOut = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const abort = () => controller.abort();
    const checkCancellation = () => {
      if (controller.signal.aborted) throw new DOMException('请求已取消', 'AbortError');
    };
    options.signal?.addEventListener('abort', abort, { once: true });
    if (options.signal?.aborted) abort();
    if (timeoutMs > 0) timer = setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs);

    try {
      checkCancellation();
      const response = await dependencies.fetch(path, {
        method: body === undefined ? 'GET' : 'POST',
        headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: 'same-origin',
        signal: controller.signal,
      });
      checkCancellation();
      // Authentication must not depend on whether an error page happens to be JSON.
      if (response.status === 401 && path !== '/api/login') dependencies.onUnauthorized();
      let data: unknown;
      try {
        data = await response.json();
      } catch {
        checkCancellation();
        if (response.ok) throw error('INVALID_RESPONSE', '服务返回了无效的 JSON 数据，请重试', response.status);
      }
      checkCancellation();
      if (!response.ok) {
        const message = data && typeof data === 'object' && 'error' in data
          && typeof data.error === 'string' && data.error ? data.error : `请求失败（HTTP ${response.status}），请重试`;
        throw error('HTTP', message, response.status);
      }
      return data;
    } catch (cause) {
      if (timedOut) throw error('TIMEOUT', '请求超时，请重试');
      checkCancellation();
      if (cause instanceof Error && cause.name === 'ApiError') throw cause;
      throw error('NETWORK', '网络连接失败，请检查连接后重试');
    } finally {
      clearTimeout(timer);
      options.signal?.removeEventListener('abort', abort);
    }
  };
}
