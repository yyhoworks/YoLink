import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

export default defineConfig({
  publicDir: false,
  plugins: [{
    name: 'yolink-classic-script',
    enforce: 'post',
    generateBundle(_options, bundle) {
      const chunk = bundle['app.js'];
      if (!chunk || chunk.type !== 'chunk' || Object.keys(bundle).length !== 1
        || chunk.imports.length || chunk.dynamicImports.length || chunk.exports.length) {
        throw new Error('YoLink requires one classic app.js without imports or exports');
      }
      // Vite treats the input as a module and strips its strict directive.
      // Restore it after all transforms: classic scripts do not imply strict mode.
      chunk.code = "'use strict';\n/* Generated from frontend/src/legacy/app.js. Do not edit. See docs/DEVELOPMENT.md. */\n" + chunk.code;
    },
  }],
  build: {
    target: ['chrome87', 'edge87', 'safari14'],
    outDir: fileURLToPath(new URL('../web/', import.meta.url)),
    emptyOutDir: false,
    copyPublicDir: false,
    minify: false,
    sourcemap: false,
    reportCompressedSize: false,
    rollupOptions: {
      input: fileURLToPath(new URL('./src/legacy/app.js', import.meta.url)),
      // Other classic scripts read and replace these top-level declarations.
      treeshake: false,
      preserveEntrySignatures: 'strict',
      output: {
        format: 'es',
        entryFileNames: 'app.js',
        inlineDynamicImports: true,
        minifyInternalExports: false,
      },
    },
  },
});
