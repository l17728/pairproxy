const { app, BrowserWindow, dialog, ipcMain } = require('electron');
const path = require('path');
const http = require('http');
const https = require('https');
const fs = require('fs');
const url = require('url');

let mainWindow = null;
let server = null;

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js':   'application/javascript',
  '.css':  'text/css',
  '.json': 'application/json',
  '.png':  'image/png',
  '.jpg':  'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif':  'image/gif',
  '.svg':  'image/svg+xml',
  '.ico':  'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf':  'font/ttf',
};

function startServer() {
  const wwwDir = path.join(__dirname, 'www');

  server = http.createServer((req, res) => {
    const parsed = url.parse(req.url);

    // CORS preflight
    if (req.method === 'OPTIONS') {
      res.writeHead(200, {
        'Access-Control-Allow-Origin': '*',
        'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
        'Access-Control-Allow-Headers': 'Authorization, Content-Type, X-Target-URL',
      });
      return res.end();
    }

    // LLM API proxy
    if (parsed.pathname === '/api/llm-proxy' && req.method === 'POST') {
      const targetUrl = req.headers['x-target-url'];
      if (!targetUrl) { res.writeHead(400); res.end('missing x-target-url'); return; }
      const target = new URL(targetUrl);
      const mod = target.protocol === 'https:' ? https : http;
      const bodyChunks = [];
      req.on('data', c => bodyChunks.push(c));
      req.on('end', () => {
        const bodyBuf = Buffer.concat(bodyChunks);
        const proxyReq = mod.request(targetUrl, {
          method: 'POST',
          headers: {
            'Content-Type': req.headers['content-type'] || 'application/json',
            'Authorization': req.headers['authorization'] || '',
            'Content-Length': bodyBuf.length,
          },
        }, (proxyRes) => {
          res.writeHead(proxyRes.statusCode, {
            'Access-Control-Allow-Origin': '*',
            'Content-Type': proxyRes.headers['content-type'] || 'text/event-stream',
            'Cache-Control': 'no-cache',
          });
          proxyRes.pipe(res);
        });
        proxyReq.on('error', e => { res.writeHead(502, {'Access-Control-Allow-Origin':'*'}); res.end(JSON.stringify({error:e.message})); });
        proxyReq.write(bodyBuf);
        proxyReq.end();
      });
      return;
    }

    // Static file serving
    let filePath = path.join(wwwDir, decodeURIComponent(parsed.pathname).split('?')[0]);
    if (filePath === wwwDir + '/' || filePath === wwwDir + '\\') filePath = path.join(wwwDir, 'generator.html');

    const resolved = path.resolve(filePath);
    if (!resolved.startsWith(path.resolve(wwwDir))) {
      res.writeHead(403);
      return res.end('Forbidden');
    }

    const ext = path.extname(filePath).toLowerCase();
    const contentType = MIME[ext] || 'application/octet-stream';

    fs.readFile(filePath, (err, data) => {
      if (err) {
        res.writeHead(404, { 'Content-Type': 'text/plain' });
        return res.end('Not found: ' + path.basename(filePath));
      }
      res.writeHead(200, { 'Content-Type': contentType, 'Access-Control-Allow-Origin': '*' });
      res.end(data);
    });
  });

  server.listen(19871, '127.0.0.1', () => {
    console.log('PPT Maker serving on http://127.0.0.1:19871');
    if (mainWindow) mainWindow.loadURL('http://127.0.0.1:19871/generator.html');
  });
}

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    minWidth: 1024,
    minHeight: 700,
    title: 'PPT 制作器',
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
    }
  });

  mainWindow.setMenuBarVisibility(false);

  // Show loading screen while server starts
  mainWindow.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(
    '<html><body style="display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;font-family:sans-serif;background:#1a1a2e;color:#fff;margin:0">'
    + '<h1 style="font-size:28px">PPT 制作器</h1>'
    + '<p style="color:#aaa;margin-top:12px">正在加载...</p>'
    + '</body></html>'
  ));

  startServer();
}

app.whenReady().then(createWindow);

app.on('window-all-closed', function() {
  if (server) server.close();
  app.quit();
});

app.on('before-quit', function() {
  if (server) server.close();
});
