import { readFileSync, readdirSync } from 'node:fs';
import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import { parse } from 'acorn';

// The legacy entry is a classic script: at runtime it shares one global scope
// with the generated bundle and every other classic script in web/. Build the
// globals its no-undef check needs from the contract fixture (the bundle's
// declared bindings) plus the top-level declarations of the sibling classic
// scripts, mirroring how classic-contract.test.ts inventories the bundle.
const classicGlobals = { lucide: 'readonly' };
try {
  const fixture = JSON.parse(readFileSync(new URL('./tests/fixtures/app-globals.json', import.meta.url), 'utf8'));
  for (const { name } of fixture) classicGlobals[name] = 'readonly';
  const webDir = new URL('../web/', import.meta.url);
  for (const file of readdirSync(webDir)) {
    if (!file.endsWith('.js') || file === 'app.js') continue;
    const tree = parse(readFileSync(new URL(file, webDir), 'utf8'), { ecmaVersion: 'latest', sourceType: 'script' });
    for (const node of tree.body) {
      if (node.type === 'VariableDeclaration') {
        for (const d of node.declarations) if (d.id.type === 'Identifier') classicGlobals[d.id.name] = node.kind === 'const' ? 'readonly' : 'writable';
      } else if (node.type === 'FunctionDeclaration' && node.id) classicGlobals[node.id.name] = 'readonly';
    }
  }
} catch { /* Fixture or scripts unavailable: legacy keeps browser globals only. */ }

const browserGlobals = {
  window: 'readonly', document: 'readonly', localStorage: 'readonly', sessionStorage: 'readonly',
  location: 'readonly', history: 'readonly', navigator: 'readonly', fetch: 'readonly',
  AbortController: 'readonly', URL: 'readonly', URLSearchParams: 'readonly',
  setTimeout: 'readonly', clearTimeout: 'readonly', setInterval: 'readonly', clearInterval: 'readonly',
  requestAnimationFrame: 'readonly', cancelAnimationFrame: 'readonly',
  addEventListener: 'readonly', removeEventListener: 'readonly', console: 'readonly',
  FormData: 'readonly', Blob: 'readonly', FileReader: 'readonly', CustomEvent: 'readonly', Event: 'readonly',
  getComputedStyle: 'readonly', matchMedia: 'readonly', MutationObserver: 'readonly',
  devicePixelRatio: 'readonly', innerWidth: 'readonly', innerHeight: 'readonly',
  alert: 'readonly', confirm: 'readonly', atob: 'readonly', btoa: 'readonly', queueMicrotask: 'readonly',
  crypto: 'readonly', performance: 'readonly', Image: 'readonly',
};

const recommendedRuleNames = new Set(Object.keys(js.configs.recommended.rules));
for (const cfg of tseslint.configs.recommended) if (cfg.rules) for (const name of Object.keys(cfg.rules)) recommendedRuleNames.add(name);

export default tseslint.config(
  { ignores: ['node_modules/**', 'test-results/**', 'playwright-report/**'] },
  js.configs.recommended,
  tseslint.configs.recommended,
  {
    files: ['**/*.mjs'],
    languageOptions: { globals: { console: 'readonly', process: 'readonly', URL: 'readonly' } },
  },
  {
    // Only no-undef is meaningful per-file for the legacy script: its
    // declarations and uses are spread across the shared classic scope, so
    // the other recommended rules (unused vars, redeclarations across
    // files, ...) cannot be judged from this file alone.
    files: ['src/legacy/**/*.js'],
    languageOptions: { globals: { ...browserGlobals, ...classicGlobals } },
    rules: {
      ...Object.fromEntries([...recommendedRuleNames].filter(name => name !== 'no-undef').map(name => [name, 'off'])),
      'no-undef': 'error',
    },
  },
);
