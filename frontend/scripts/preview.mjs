import { createServer } from 'node:http';
import { Buffer } from 'node:buffer';
import { readFile } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { extname, resolve, sep } from 'node:path';
import { fixtureAPI } from '../tests/fixtures/api.mjs';
import { butterFixture } from '../tests/fixtures/butter.mjs';

const defaultRoot = fileURLToPath(new URL('../../web/', import.meta.url));
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.png': 'image/png',
  '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.json': 'application/json' };

export function createPreviewServer(webRoot = defaultRoot) {
  const root = resolve(webRoot);
  return createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'");
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader('X-YoLink-Preview', 'synthetic-read-only');
    if (!['GET', 'HEAD'].includes(req.method)) {
      req.resume();
      res.writeHead(405, { Allow: 'GET, HEAD', 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'Synthetic preview is read-only' }));
      return;
    }
    try {
      const path = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
      if (path.startsWith('/api/')) {
        const data = process.env.YOLINK_PREVIEW_SCENARIO === 'butter' ? butterFixture(path) : fixtureAPI(path);
        res.writeHead(data === undefined ? 404 : 200, { 'Content-Type': 'application/json' });
        res.end(req.method === 'HEAD' ? undefined : JSON.stringify(data ?? { error: 'Unknown fixture API' }));
        return;
      }
      const file = resolve(root, '.' + (path === '/' ? '/index.html' : path));
      if (!file.startsWith(root + sep)) {
        res.writeHead(403).end();
        return;
      }
      let bytes = await readFile(file);
      if(path==='/index.html'||path==='/')bytes=Buffer.from(bytes.toString().replace('<body>','<body><div class="fixture-notice">本地验收 · 合成数据 · 不连接真实网盘</div>'));
      res.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream' });
      res.end(req.method === 'HEAD' ? undefined : bytes);
    } catch (error) {
      res.writeHead(error.code === 'ENOENT' || error.code === 'EISDIR' ? 404 : 400).end();
    }
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const port = Number(process.env.YOLINK_PREVIEW_PORT ?? 18797);
  const server = createPreviewServer();
  server.on('error', error => { console.error(error); process.exitCode = 1; });
  server.listen(port, '127.0.0.1', () => {
    console.log(`Synthetic read-only YoLink preview: http://127.0.0.1:${port}/`);
  });
}
