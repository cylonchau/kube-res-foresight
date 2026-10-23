// A static server for the built SPA (web/dist) that proxies /api (and the WebSocket upgrades) to the backend under test.
import http from 'node:http';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';

const DIST = path.resolve(process.env.E2E_DIST || 'web/dist');
const API = { host: '127.0.0.1', port: Number(process.env.E2E_API_PORT || 20111) };
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json', '.woff2': 'font/woff2', '.ico': 'image/x-icon' };

const server = http.createServer((req, res) => {
  if (req.url.startsWith('/api')) {
    const upstream = http.request({ ...API, path: req.url, method: req.method, headers: req.headers }, r => {
      res.writeHead(r.statusCode, r.headers);
      r.pipe(res);
    });
    upstream.on('error', e => {
      res.writeHead(502);
      res.end(String(e));
    });
    req.pipe(upstream);
    return;
  }
  let file = path.join(DIST, decodeURIComponent(req.url.split('?')[0]));
  if (!file.startsWith(DIST) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(DIST, 'index.html');
  res.writeHead(200, { 'Content-Type': types[path.extname(file)] || 'application/octet-stream' });
  fs.createReadStream(file).pipe(res);
});

server.on('upgrade', (req, socket, head) => {
  const upstream = net.connect(API.port, API.host, () => {
    upstream.write(`${req.method} ${req.url} HTTP/1.1\r\n` + Object.entries(req.headers).map(([k, v]) => `${k}: ${v}`).join('\r\n') + '\r\n\r\n');
    upstream.write(head);
    socket.pipe(upstream).pipe(socket);
  });
  upstream.on('error', () => socket.destroy());
  socket.on('error', () => upstream.destroy());
});

const PORT = Number(process.env.E2E_PORT || 20112);
server.listen(PORT, () => console.log(`serving on ${PORT}`));
