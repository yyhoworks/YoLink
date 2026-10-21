// Synthetic, non-sensitive data for read-only UI smoke tests. Never a live proxy.
const now = Date.UTC(2026, 9, 19, 4, 0);
const overview = { media: 0, outputs: 0, running: 0, queued: 0, failed: 0 };
const accounts = [{ id: 'fixture', name: '<Fixture & cloud>', provider: '139', disabled: false }];

export function fixtureAPI(path) {
  if (path === '/api/state') return {
    accounts, rules: [], jobs: [], failures: [], media: [], settings: { public_base: '', output_root: '/strm' },
    overview, reports: {}, playback: { history: [], cache: {} }, ingest: [], event_token: 'fixture-only',
  };
  if (path === '/api/dashboard') return {
    time: now, metrics_since: new Date(now).toISOString(), overview,
    stats: { today_created: 0, requests: 0, resolved: 0, resolve_total: 0, resolve_ms: 0, cache: 0, upstream_ms: 0, upstream_count: 0 },
    accounts, reports: {}, plans: [], jobs: [], recent: [], errors: [], write_error: '',
    trend: Array.from({ length: 7 }, (_, i) => ({ day: `2026-10-${13 + i}`, created: 0, requests: 0, covered: true })),
  };
  if (path === '/api/notifications') return { items: [] };
  if (path === '/api/restore/status') return { supported: true, restricted: false, runtime: {}, pending: null, upload: null };
  if (path === '/api/records') return {
    items: [{ id: 'fixture-record', kind: 'generation', event: 'strm', outcome: 'created',
      name: '<Fixture & movie>.mkv', media: 'fixture-media', time: now, path: '/synthetic/movie.mkv', output: '', note: 'Synthetic smoke record' }],
    page: 1, pages: 1, total: 1, write_error: '',
  };
  if (path === '/api/records/trace') return {
    generation: [{ name: '<Fixture & movie>.mkv', kind: 'generation', event: 'strm', outcome: 'created', time: now, millis: 0 }],
    playback: [], direct: [],
  };
  if (path === '/api/integrations') return { config: {}, deliveries: [] };
  if (path === '/api/automation') return { connection: { enabled: false }, targets: [], pipeline: [] };
  return undefined;
}
