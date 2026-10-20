// Keep the original conversion and replacement behavior, including thrown errors.
export const esc = (v: unknown): string => String(v ?? '').replace(/[&<>"']/g,
  c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);

// Unknown status values retain the legacy muted fallback.
export const jobClass = (j: { status?: unknown }): 'good' | 'error' | 'muted' =>
  j.status === 'completed' ? 'good'
    : (['failed', 'partial', 'interrupted'] as readonly unknown[]).includes(j.status) ? 'error'
      : j.status === 'running' ? 'good' : 'muted';
