/** Own one page's pending requests and polling; tokens also reject stale results. */
export function createCleanupScope() {
  let disposed = false;
  const requests = new Map<string, AbortController>();
  const intervals = new Set<ReturnType<typeof setInterval>>();
  const cancel = (key: string) => {
    const controller = requests.get(key);
    requests.delete(key);
    controller?.abort();
  };
  return {
    latest(key: string) {
      cancel(key);
      const controller = new AbortController();
      if (disposed) controller.abort();
      else requests.set(key, controller);
      return {
        signal: controller.signal,
        current: () => !disposed && !controller.signal.aborted && requests.get(key) === controller,
        finish() { if (requests.get(key) === controller) requests.delete(key); },
      };
    },
    pending: (key: string) => requests.has(key),
    cancel,
    interval(callback: () => void, milliseconds: number) {
      if (!disposed) intervals.add(setInterval(() => { if (!disposed) callback(); }, milliseconds));
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      for (const key of requests.keys()) cancel(key);
      for (const timer of intervals) clearInterval(timer);
      intervals.clear();
    },
  };
}
