const { app, BrowserWindow, dialog, ipcMain } = require('electron');
const path = require('path');
const http = require('http');
const https = require('https');
const fs = require('fs');
const url = require('url');

let mainWindow = null;
let server = null;
let currentRootDir = null;
let injectRegistered = false;

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
  '.mp3':  'audio/mpeg',
  '.wav':  'audio/wav',
  '.md':   'text/markdown; charset=utf-8',
  '.txt':  'text/plain; charset=utf-8',
  '.mp4':  'video/mp4',
  '.webm': 'video/webm',
  '.m4a':  'audio/mp4',
  '.ico':  'image/x-icon',
};

function startServer(rootDir) {
  if (server) { server.close(); server = null; }
  currentRootDir = rootDir;

  server = http.createServer((req, res) => {
    const parsed = url.parse(req.url);

    // LLM API proxy - bypass CORS
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
          res.writeHead(proxyRes.statusCode, proxyRes.headers);
          proxyRes.pipe(res);
        });
        proxyReq.on('error', e => { res.writeHead(502); res.end('proxy error: ' + e.message); });
        proxyReq.write(bodyBuf);
        proxyReq.end();
      });
      return;
    }

    // Save slide content to MD file
    if (parsed.pathname === '/api/save-slide' && req.method === 'POST') {
      const chunks = [];
      req.on('data', c => chunks.push(c));
      req.on('end', () => {
        try {
          const body = JSON.parse(Buffer.concat(chunks).toString());
          const slideDir = path.join(rootDir, body.dir);
          const mdPath = path.join(slideDir, 'slide.md');
          if (!mdPath.startsWith(path.resolve(rootDir))) { res.writeHead(403); res.end('Forbidden'); return; }
          fs.mkdirSync(slideDir, { recursive: true });
          fs.writeFileSync(mdPath, body.content, 'utf-8');
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end('{"ok":true}');
        } catch (e) {
          res.writeHead(500, { 'Content-Type': 'application/json' });
          res.end('{"error":"' + e.message + '"}');
        }
      });
      return;
    }

    // Save manifest
    if (parsed.pathname === '/api/save-manifest' && req.method === 'POST') {
      const chunks = [];
      req.on('data', c => chunks.push(c));
      req.on('end', () => {
        try {
          const body = JSON.parse(Buffer.concat(chunks).toString());
          const mfPath = path.join(rootDir, body.dir, 'manifest.json');
          if (!mfPath.startsWith(path.resolve(rootDir))) { res.writeHead(403); res.end('Forbidden'); return; }
          fs.writeFileSync(mfPath, JSON.stringify(body.data, null, 2), 'utf-8');
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end('{"ok":true}');
        } catch (e) {
          res.writeHead(500, { 'Content-Type': 'application/json' });
          res.end('{"error":"' + e.message + '"}');
        }
      });
      return;
    }

    let filePath = path.join(rootDir, decodeURIComponent(parsed.pathname));

    const resolved = path.resolve(filePath);
    if (!resolved.startsWith(path.resolve(rootDir))) {
      res.writeHead(403);
      res.end('Forbidden');
      return;
    }

    if (parsed.pathname === '/' || parsed.pathname === '') {
      filePath = path.join(rootDir, 'slides.html');
    }

    const ext = path.extname(filePath).toLowerCase();
    const contentType = MIME[ext] || 'application/octet-stream';

    // Safe stat check
    fs.stat(filePath, (statErr, stat) => {
      if (statErr) {
        res.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end('<html><body style="display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;font-family:sans-serif;background:#1a1a2e;color:#fff;margin:0">'
          + '<h2>404 - 未找到 slides.html</h2>'
          + '<p style="color:#aaa;margin:16px 0">请确认选择的目录包含 slides.html 文件</p>'
          + '<button onclick="window.electronAPI.openFolder()" style="padding:12px 32px;font-size:16px;border:2px solid #6c63ff;background:transparent;color:#fff;border-radius:8px;cursor:pointer">📂 重新选择目录</button>'
          + '</body></html>');
        return;
      }

      const range = req.headers.range;
      if (range && (ext === '.mp3' || ext === '.wav' || ext === '.m4a')) {
        const parts = range.replace(/bytes=/, '').split('-');
        const start = parseInt(parts[0], 10);
        const end = parts[1] ? parseInt(parts[1], 10) : stat.size - 1;
        res.writeHead(206, {
          'Content-Range': 'bytes ' + start + '-' + end + '/' + stat.size,
          'Accept-Ranges': 'bytes',
          'Content-Length': end - start + 1,
          'Content-Type': contentType,
        });
        fs.createReadStream(filePath, { start, end }).pipe(res);
      } else {
        fs.readFile(filePath, (err, data) => {
          if (err) {
            res.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
            res.end('<html><body style="display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;font-family:sans-serif;background:#1a1a2e;color:#fff;margin:0">'
              + '<h2>404 - 文件未找到</h2>'
              + '<p style="color:#aaa;margin:16px 0">' + path.basename(filePath) + '</p>'
              + '<button onclick="window.electronAPI.openFolder()" style="padding:12px 32px;font-size:16px;border:2px solid #6c63ff;background:transparent;color:#fff;border-radius:8px;cursor:pointer">📂 重新选择目录</button>'
              + '</body></html>');
            return;
          }
          res.writeHead(200, { 'Content-Type': contentType, 'Cache-Control': 'no-cache' });
          res.end(data);
        });
      }
    });
  });

  server.listen(19870, '127.0.0.1', () => {
    console.log('Serving ' + rootDir + ' on http://127.0.0.1:19870');
    loadSlides(19870);
  });
}

function loadSlides(port) {
  if (!mainWindow) return;
  mainWindow.loadURL('http://127.0.0.1:' + port + '/slides.html');
}

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    minWidth: 1024,
    minHeight: 700,
    title: '课件查看器',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
    }
  });

  mainWindow.setMenuBarVisibility(false);
  mainWindow.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(getWelcomeHTML()));
}

function getWelcomeHTML() {
  return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>课件查看器</title>'
    + '<style>'
    + '*{margin:0;padding:0;box-sizing:border-box;}'
    + 'body{font-family:-apple-system,"Microsoft YaHei",sans-serif;'
    + 'background:linear-gradient(135deg,#0f0c29,#302b63,#24243e);'
    + 'color:#fff;display:flex;flex-direction:column;align-items:center;'
    + 'justify-content:center;height:100vh;text-align:center;}'
    + 'h1{font-size:36px;margin-bottom:12px;font-weight:700;}'
    + 'p{font-size:16px;color:#aaa;margin-bottom:40px;}'
    + '.btn{padding:14px 48px;font-size:18px;border:2px solid #6c63ff;'
    + 'background:transparent;color:#fff;border-radius:8px;cursor:pointer;transition:all 0.2s;}'
    + '.btn:hover{background:#6c63ff;transform:scale(1.05);}'
    + '.hint{margin-top:24px;font-size:13px;color:#666;}'
    + '</style></head><body>'
    + '<h1>课件查看器</h1>'
    + '<p>选择包含 slides.html 的资源目录即可开始播放</p>'
    + '<button class="btn" onclick="openFolder()">📂 选择资源目录</button>'
    + '<div class="hint">支持自定义课件资源，将新的材料复制到任意目录后打开即可</div>'
    + '<script>function openFolder(){window.electronAPI.openFolder();}<\/script>'
    + '</body></html>';
}

// IPC
ipcMain.handle('open-folder', async function() {
  var result = await dialog.showOpenDialog(mainWindow, {
    title: '选择课件资源目录',
    properties: ['openDirectory'],
    buttonLabel: '选择此目录'
  });
  if (result.canceled || result.filePaths.length === 0) return null;

  var dir = result.filePaths[0];

  // If no slides.html in selected dir, search parent and children
  if (!fs.existsSync(path.join(dir, 'slides.html'))) {
    // Check parent
    if (fs.existsSync(path.join(dir, '..', 'slides.html'))) {
      dir = path.resolve(dir, '..');
    } else {
      // Check if there's a child dir with slides.html
      var found = false;
      try {
        var entries = fs.readdirSync(dir, { withFileTypes: true });
        for (var i = 0; i < entries.length; i++) {
          if (entries[i].isDirectory()) {
            var candidate = path.join(dir, entries[i].name, 'slides.html');
            if (fs.existsSync(candidate)) {
              dir = path.join(dir, entries[i].name);
              found = true;
              break;
            }
          }
        }
      } catch(e) {}
      if (!found) {
        // Still no slides.html - serve it anyway, 404 page will show re-select button
      }
    }
  }

  startServer(dir);
  return dir;
});

ipcMain.handle('get-current-dir', function() { return currentRootDir; });

ipcMain.handle('set-fullscreen', function(event, flag) {
  if (mainWindow) mainWindow.setFullScreen(flag);
});

ipcMain.handle('quit', function() {
  if (mainWindow) mainWindow.close();
});

app.whenReady().then(createWindow);

app.on('window-all-closed', function() {
  if (server) server.close();
  app.quit();
});

app.on('before-quit', function() {
  if (server) server.close();
});
