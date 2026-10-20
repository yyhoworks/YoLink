import { readFile } from 'node:fs/promises';
import { Buffer } from 'node:buffer';
import { build } from 'vite';

// write:false keeps verification entirely in memory, including on failure.
const result = await build({ build: { write: false }, logLevel: 'warn' });
if (Array.isArray(result) || !('output' in result)) {
  throw new Error('Expected a single frontend bundle');
}
if (result.output.length !== 1 || result.output[0].type !== 'chunk'
  || result.output[0].fileName !== 'app.js') {
  throw new Error('Expected app.js only, without chunks, maps or extra assets');
}
const actual = await readFile(new URL('../../web/app.js', import.meta.url));
if (!actual.equals(Buffer.from(result.output[0].code))) {
  throw new Error('web/app.js is stale. Run npm run --prefix frontend build');
}
console.log('web/app.js matches a fresh, in-memory build');
