const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('electronAPI', {
  openFolder: () => ipcRenderer.invoke('open-folder'),
  getCurrentDir: () => ipcRenderer.invoke('get-current-dir'),
  setFullscreen: (flag) => ipcRenderer.invoke('set-fullscreen', flag),
  quit: () => ipcRenderer.invoke('quit'),
});
