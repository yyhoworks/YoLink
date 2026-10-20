import { readFileSync } from 'node:fs';
import { Script } from 'node:vm';
import { parse } from 'acorn';
import { describe, expect, it } from 'vitest';

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');
const generated = read('../../web/app.js');

describe('classic script migration contract', () => {
  it('keeps original declarations and the explicit API/lifecycle infrastructure bindings', () => {
    const tree = parse(generated, { ecmaVersion: 'latest', sourceType: 'script' });
    const bindings = tree.body.flatMap(node => {
      if (node.type === 'VariableDeclaration') {
        return node.declarations.map(d => {
          if (d.id.type !== 'Identifier') throw new Error('Unexpected global destructuring');
          return { name: d.id.name, kind: node.kind };
        });
      }
      return node.type === 'FunctionDeclaration' && node.id
        ? [{ name: node.id.name, kind: 'function' }] : [];
    });
    const original = JSON.parse(read('./fixtures/app-globals.json'));
    const sort = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name);
    expect(bindings.sort(sort)).toEqual([
      ...original,
      { name: 'createAPI', kind: 'function' },
      { name: 'requestAPI', kind: 'const' },
      { name: 'createCleanupScope', kind: 'function' },
      { name: 'refreshLifecycle', kind: 'const' },
      { name: 'refreshDelay', kind: 'function' },
      { name: 'scheduleRefresh', kind: 'function' },
      { name: 'preserveControlFocus', kind: 'function' },
    ].sort(sort));
    expect(tree.body[0]).toMatchObject({ type: 'ExpressionStatement', directive: 'use strict' });
  });

  it('keeps the ordered classic scripts and loads reliability after its dependencies', () => {
    const scripts = [...read('../../web/index.html').matchAll(/<script\b([^>]*)>/g)];
    expect(scripts.map(([, attrs]) => /\bsrc="([^"]+)"/.exec(attrs)?.[1].split('?')[0])).toEqual([
      '/assets/lucide.min.js', '/generation.js', '/clouds.js',
      '/playback-ui.js', '/records.js', '/app.js',
      '/console.js', '/automation.js',
      '/workbench.js', '/connections.js',
      '/glass-color.js', '/appearance.js', '/butter.js', '/reliability.js',
    ]);
    for (const [, attrs] of scripts) {
      expect(attrs).toMatch(/\bdefer\b/);
      expect(attrs).not.toMatch(/\btype\s*=/);
    }
  });

  it('parses the whole ordered script set in one shared classic scope', () => {
    const urls = [...read('../../web/index.html').matchAll(/<script\b[^>]*src="([^"]+)"/g)];
    const source = urls.map(([, url]) => read('../../web' + url.split('?')[0])).join('\n;\n');
    expect(() => new Script(source)).not.toThrow();
    expect(generated).not.toMatch(/sourceMappingURL|https?:\/\/.*(?:unpkg|jsdelivr)/);
    expect(generated).toContain('Generated from frontend/src/legacy/app.js');
  });

  it('imports the pure functions once and leaves no duplicate legacy implementation', () => {
    const source = read('../src/legacy/app.js');
    expect(source).toContain("import { esc, jobClass } from '../shared/presentation.ts'");
    expect(source).not.toMatch(/(?:const|function)\s+(?:esc|jobClass)\b/);
  });
});
