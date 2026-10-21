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
      // Returns a cancel function for this timer only; dispose() still
      // clears every timer that was not cancelled. A throwing callback is
      // reported and isolated so one bad tick cannot kill the interval.
      if (disposed) return () => {};
      const timer = setInterval(() => {
        if (disposed) return;
        try { callback(); } catch (error) { console.error('cleanup scope interval callback failed', error); }
      }, milliseconds);
      intervals.add(timer);
      return () => { clearInterval(timer); intervals.delete(timer); };
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
