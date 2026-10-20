import { describe, expect, it } from 'vitest';
import { esc, jobClass } from '../src/shared/presentation';

describe('esc: legacy HTML escaping', () => {
  it.each([
    [null, ''], [undefined, ''], ['', ''], [0, '0'], [false, 'false'],
    [NaN, 'NaN'], [Infinity, 'Infinity'], [-Infinity, '-Infinity'],
    ['普通文本 / 路径', '普通文本 / 路径'],
    ['&<>"\'', '&amp;&lt;&gt;&quot;&#39;'],
    ['&amp;', '&amp;amp;'],
    ['<script>alert("x")</script>', '&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;'],
    ['a\nb\\c', 'a\nb\\c'],
    [12n, '12'], [Symbol('id'), 'Symbol(id)'],
    [[1, '<'], '1,&lt;'],
  ])('esc(%s) preserves existing conversions', (input, expected) => {
    expect(esc(input)).toBe(expected);
  });

  it('uses String conversion without swallowing conversion errors', () => {
    expect(esc({ toString: () => '<custom>' })).toBe('&lt;custom&gt;');
    const error = new Error('conversion failed');
    expect(() => esc({ toString() { throw error; } })).toThrow(error);
  });
});

describe('jobClass: legacy status presentation', () => {
  it.each([
    ['completed', 'good'], ['running', 'good'],
    ['failed', 'error'], ['partial', 'error'], ['interrupted', 'error'],
    ['queued', 'muted'], ['cancelled', 'muted'], ['unknown', 'muted'],
    ['', 'muted'], [undefined, 'muted'], [null, 'muted'], [0, 'muted'],
  ])('%s maps to %s', (status, expected) => {
    expect(jobClass({ status })).toBe(expected);
  });

  it('keeps the missing-status fallback and invalid-job errors', () => {
    expect(jobClass({})).toBe('muted');
    // @ts-expect-error Characterize JS callers that pass an invalid job.
    expect(() => jobClass(null)).toThrow(TypeError);
    // @ts-expect-error Characterize JS callers that pass an invalid job.
    expect(() => jobClass(undefined)).toThrow(TypeError);
  });
});
