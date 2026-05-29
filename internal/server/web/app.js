let ws = null;
let selectedPeerId = null;
let peers = {};
let transfers = {};
let myId = null;
let reconnectDelay = 1000;
let wsUploadSocket = null;

const autoDownloadEl = document.getElementById('autoDownload');

// Restore auto-download preference
const savedAutoDl = localStorage.getItem('autoDownload');
if (savedAutoDl !== null) {
  autoDownloadEl.checked = savedAutoDl === 'true';
}
autoDownloadEl.addEventListener('change', () => {
  localStorage.setItem('autoDownload', autoDownloadEl.checked);
});

// WAN toggle
const wanToggleEl = document.getElementById('wanToggle');
wanToggleEl.addEventListener('change', () => {
  fetch('/api/wan', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({enabled: wanToggleEl.checked})
  })
    .then(r => r.json())
    .then(data => {
      wanToggleEl.checked = data.wan_enabled;
      showToast(data.wan_enabled ? 'WAN enabled — short codes work across networks' : 'WAN disabled — LAN only', 'success');
    })
    .catch(() => {
      wanToggleEl.checked = !wanToggleEl.checked;
      showToast('Failed to toggle WAN mode', 'error');
    });
});

function setConnStatus(state) {
  const el = document.getElementById('connStatus');
  el.className = 'conn-status ' + state;
  el.querySelector('.conn-label').textContent =
    state === 'connected' ? 'Connected' :
    state === 'reconnecting' ? 'Reconnecting...' : 'Disconnected';

  const mEl = document.getElementById('mobileConnStatus');
  if (mEl) {
    mEl.className = 'conn-status ' + state;
    mEl.querySelector('.conn-label').textContent =
      state === 'connected' ? 'Connected' :
      state === 'reconnecting' ? 'Reconnecting...' : 'Disconnected';
  }
}

function connect() {
  setConnStatus('reconnecting');
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws`);

  ws.onopen = () => {
    console.log('connected');
    setConnStatus('connected');
    reconnectDelay = 1000;
  };

  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    handleMsg(msg);
  };

  ws.onclose = () => {
    setConnStatus('disconnected');
    setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 1.5, 10000);
  };

  ws.onerror = () => {
    ws.close();
  };
}

function handleMsg(msg) {
  switch (msg.type) {
    case 'my_id':
      myId = msg.id;
      document.getElementById('peerId').textContent = msg.id;
      document.getElementById('shortCode').textContent = '#' + msg.short_code;
      
      const mPeerId = document.getElementById('mobilePeerId');
      if (mPeerId) mPeerId.textContent = msg.id;
      const mShort = document.getElementById('mobileShortCode');
      if (mShort) mShort.textContent = '#' + msg.short_code;

      loadMultiaddr();
      loadPeersAndConnections();
      break;
    case 'peer_found':
    case 'peer_lost':
    case 'peer_connected':
    case 'peer_disconnected':
      loadPeersAndConnections();
      break;
    case 'transfer_new':
      transfers[msg.transfer.id] = msg.transfer;
      renderTransfers();
      break;
    case 'transfer_progress':
      if (transfers[msg.transfer.id]) {
        transfers[msg.transfer.id] = msg.transfer;
        renderTransfers();
      }
      break;
    case 'transfer_complete':
      transfers[msg.transfer.id] = msg.transfer;
      renderTransfers();
      if (msg.transfer.direction === 'receive' && autoDownloadEl.checked) {
        downloadFile(msg.transfer.id, msg.transfer.filename);
      }
      showToast(`Transfer complete: ${msg.transfer.filename}`, 'success');
      break;
    case 'transfer_error':
      if (transfers[msg.id]) {
        transfers[msg.id].status = 'failed';
        renderTransfers();
      }
      showToast(`Transfer failed: ${msg.message}`, 'error');
      break;
  }
}

function loadMultiaddr() {
  fetch('/api/info')
    .then(r => r.json())
    .then(info => {
      const addrEl = document.getElementById('multiaddr');
      if (info.addrs && info.addrs.length > 0) {
        const addr = info.addrs.find(a => !a.includes('127.0.0.1') && !a.includes('::1')) || info.addrs[0];
        addrEl.textContent = addr;
        addrEl.title = addr;
      }
      const codeEl = document.getElementById('connectionCode');
      if (info.connection_code && codeEl) {
        codeEl.textContent = info.connection_code;
        codeEl.title = info.connection_code;
      }
      const mCodeEl = document.getElementById('mobileConnectionCode');
      if (info.connection_code && mCodeEl) {
        mCodeEl.textContent = info.connection_code;
        mCodeEl.title = info.connection_code;
      }
      // WAN toggle
      const wanEl = document.getElementById('wanToggle');
      if (wanEl) wanEl.checked = !!info.wan_enabled;
      const mWanEl = document.getElementById('mobileWanToggle');
      if (mWanEl) mWanEl.checked = !!info.wan_enabled;

      // Update app version in UI
      if (info.version) {
        const verEl = document.getElementById('appVersion');
        if (verEl) verEl.textContent = info.version;
        const mVerEl = document.getElementById('mobileVersion');
        if (mVerEl) mVerEl.textContent = info.version;
      }
    })
    .catch(() => {});
}

function getPeerColor(id) {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = id.charCodeAt(i) + ((hash << 5) - hash);
  }
  const h = Math.abs(hash % 360);
  return `hsl(${h}, 65%, 55%)`;
}

let connectedPeers = {};

function loadPeersAndConnections() {
  fetch('/api/peers')
    .then(r => r.json())
    .then(data => {
      peers = {};
      if (data.peers) {
        data.peers.forEach(p => {
          peers[p.id] = p;
        });
      }

      connectedPeers = {};
      if (data.connected_peers) {
        data.connected_peers.forEach(p => {
          connectedPeers[p.id] = p;
        });
      }

      renderPeers();
      renderConnectedPeers();
    })
    .catch(() => {});
}

function connectToPeer(addr) {
  showToast('Connecting... / 正在连接...', 'info');
  fetch('/api/peers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ addr }),
  })
    .then(r => {
      if (r.ok) {
        showToast('Connection established / 连接成功!', 'success');
        loadPeersAndConnections();
      } else {
        r.text().then(t => showToast(`Connect failed: ${t}`, 'error'));
      }
    })
    .catch(() => showToast('Connect failed', 'error'));
}

function renderPeers() {
  const list = document.getElementById('peerList');
  const mList = document.getElementById('mobilePeerList');
  const ids = Object.keys(peers).filter(id => id !== myId);
  document.getElementById('peerCount').textContent = ids.length;

  const ownShort = document.getElementById('shortCode').textContent.replace('#', '');
  const ownItem = myId ? `
    <li class="own-device-item" style="border: 1px dashed var(--primary); cursor: default; opacity: 0.9; margin-bottom: 8px; display:flex; align-items:center; gap:12px; padding: 12px 14px; border-radius: var(--radius-md); background: rgba(99, 102, 241, 0.04);">
      <div class="peer-avatar" style="background:var(--primary); font-size:11px; font-weight:800; color: #fff;">本机</div>
      <div class="peer-info-container">
        <div class="peer-name" style="color:var(--primary); font-weight:800;">Device #${ownShort} (本机)</div>
        <div class="peer-id-short">${myId.slice(0, 12)}...${myId.slice(-6)}</div>
      </div>
    </li>
  ` : '';

  // 1. Render Radar Screen
  const radarWrapper = document.getElementById('radar-peers');
  if (radarWrapper) {
    if (ids.length === 0) {
      radarWrapper.innerHTML = '';
      document.getElementById('radarStateText').textContent = 'Searching for devices... / 正在搜寻设备...';
    } else {
      document.getElementById('radarStateText').textContent = `Discovered ${ids.length} device(s) / 发现 ${ids.length} 台设备`;
      radarWrapper.innerHTML = ids.map((id, index) => {
        const p = peers[id];
        const isConnected = !!connectedPeers[id] || p.connected;
        const short = p.short_code || id.slice(-8);
        const initial = short.slice(-2).toUpperCase();
        const color = getPeerColor(id);
        
        // Circular math to arrange elements cleanly
        const angle = (index * (2 * Math.PI) / ids.length) + (Math.PI / 4);
        const radius = 80 + (index % 2) * 16;
        const x = Math.round(50 + Math.cos(angle) * (radius / 2.4));
        const y = Math.round(50 + Math.sin(angle) * (radius / 2.4));
        const connClass = isConnected ? ' connected' : '';
        
        return `<div class="radar-peer-node${connClass}" data-id="${id}" style="left:${x}%; top:${y}%;">
          <div class="radar-peer-avatar" style="background:${color};">${initial}</div>
          <span class="radar-peer-name">#${short}</span>
        </div>`;
      }).join('');

      radarWrapper.querySelectorAll('.radar-peer-node').forEach(node => {
        node.addEventListener('click', () => {
          const id = node.dataset.id;
          const p = peers[id];
          const isConnected = !!connectedPeers[id] || p.connected;
          if (!isConnected) {
            connectToPeer(id);
          } else {
            selectedPeerId = id;
            renderConnectedPeers();
            showToast('Selected device / 已选中设备', 'success');
          }
        });
      });
    }
  }

  // 2. Render Lists
  const emptyItem = '<li class="empty-state" style="padding:16px 8px;text-align:center;font-size:12px;color:var(--text-muted);width:100%;">No other devices found / 暂无其他设备</li>';
  const listHTML = ids.length === 0 ? ownItem + emptyItem : ownItem + ids.map(id => {
    const p = peers[id];
    const isConnected = !!connectedPeers[id] || p.connected;
    const short = p.short_code || id.slice(-8);
    const friendly = `Device #${short}`;
    const statusText = isConnected ? '<span style="font-size:10px; color:var(--success); font-weight:700;">● Connected</span>' : '<span style="font-size:10px; color:var(--text-muted);">○ Click to Connect</span>';
    const color = getPeerColor(id);
    const initial = short.slice(-2).toUpperCase();
    
    return `<li data-id="${id}" data-connected="${isConnected}" style="margin-bottom: 8px; display:flex; align-items:center; gap:12px; padding: 12px 14px; border-radius: var(--radius-md); background: var(--surface); border: 1px solid transparent; cursor: pointer; transition: var(--transition);">
      <div class="peer-avatar" style="background:${color}">${initial}</div>
      <div class="peer-info-container">
        <div class="peer-name">${friendly}</div>
        <div class="peer-id-short">${id.slice(0, 12)}...${id.slice(-6)}</div>
        <div style="margin-top:2px;">${statusText}</div>
      </div>
    </li>`;
  }).join('');

  if (list) list.innerHTML = listHTML;
  if (mList) mList.innerHTML = listHTML;

  const bindClicks = (el) => {
    if (!el) return;
    el.querySelectorAll('li[data-id]').forEach(li => {
      li.addEventListener('click', () => {
        const id = li.dataset.id;
        const isConnected = li.dataset.connected === 'true';
        if (!isConnected) {
          connectToPeer(id);
        } else {
          selectedPeerId = id;
          renderConnectedPeers();
          showToast('Selected device for transfer / 已选中该传输设备', 'success');
        }
      });
    });
  };

  bindClicks(list);
  bindClicks(mList);
}

function renderConnectedPeers() {
  const list = document.getElementById('connectedPeerList');
  const mList = document.getElementById('mobileConnectedPeerList');
  const ids = Object.keys(connectedPeers);
  document.getElementById('connectedCount').textContent = ids.length;

  const emptyState = '<li class="empty-state" style="padding:16px 8px;text-align:center;font-size:12px;color:var(--text-muted);width:100%;">No active connections / 暂无连接</li>';
  const listHTML = ids.length === 0 ? emptyState : ids.map(id => {
    const p = connectedPeers[id];
    const short = p.short_code || id.slice(-8);
    const friendly = `Device #${short}`;
    const selected = id === selectedPeerId ? ' selected' : '';
    const color = getPeerColor(id);
    const initial = short.slice(-2).toUpperCase();
    return `<li data-id="${id}" class="${selected}" style="position:relative; display:flex; align-items:center; gap:12px; padding: 12px 14px; border-radius: var(--radius-md); background: var(--surface); border: 1px solid transparent; cursor: pointer; transition: var(--transition); width:100%;">
      <div class="peer-avatar" style="background:${color}">${initial}</div>
      <div class="peer-info-container" style="flex:1; min-width:0;">
        <div class="peer-name">${friendly}</div>
        <div class="peer-id-short">${id.slice(0, 12)}...${id.slice(-6)}</div>
      </div>
      <button class="disconnect-btn icon-btn" data-id="${id}" title="Disconnect / 断开连接" style="margin-left:auto; z-index:2; padding:6px; color:var(--error); border:none; background:transparent; cursor:pointer;">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
          <line x1="18" y1="6" x2="6" y2="18"></line>
          <line x1="6" y1="6" x2="18" y2="18"></line>
        </svg>
      </button>
    </li>`;
  }).join('');

  if (list) list.innerHTML = listHTML;
  if (mList) mList.innerHTML = listHTML;

  const bindClicks = (el) => {
    if (!el) return;
    el.querySelectorAll('li[data-id]').forEach(li => {
      li.addEventListener('click', (e) => {
        if (e.target.closest('.disconnect-btn')) return;
        selectedPeerId = li.dataset.id;
        renderConnectedPeers();
      });
    });

    el.querySelectorAll('.disconnect-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const id = btn.dataset.id;
        disconnectPeer(id);
      });
    });
  };

  bindClicks(list);
  bindClicks(mList);
}

function disconnectPeer(id) {
  showToast('Disconnecting... / 正在断开...', 'info');
  fetch('/api/peers/disconnect', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ addr: id }),
  })
    .then(r => {
      if (r.ok) {
        showToast('Disconnected / 已断开连接', 'success');
        if (selectedPeerId === id) selectedPeerId = null;
        loadPeersAndConnections();
      } else {
        r.text().then(t => showToast(`Disconnect failed: ${t}`, 'error'));
      }
    })
    .catch(() => showToast('Disconnect failed', 'error'));
}

function renderTransfers() {
  const list = document.getElementById('transferList');
  const mList = document.getElementById('mobileTransferList');
  const empty = document.getElementById('emptyTransfers');
  const mEmpty = document.getElementById('mobileEmptyTransfers');
  const arr = Object.values(transfers);

  const activeCount = arr.filter(t => t.status === 'pending' || t.status === 'transferring').length;
  const badge = document.getElementById('mobileActiveBadge');
  if (badge) {
    badge.textContent = activeCount;
    badge.classList.toggle('hidden', activeCount === 0);
  }

  if (arr.length === 0) {
    if (list) list.innerHTML = '';
    if (mList) mList.innerHTML = '';
    if (empty) empty.style.display = 'block';
    if (mEmpty) mEmpty.style.display = 'block';
    return;
  }
  if (empty) empty.style.display = 'none';
  if (mEmpty) mEmpty.style.display = 'none';

  const updateList = (targetList) => {
    if (!targetList) return;
    arr.forEach(t => {
      const pct = t.size > 0 ? Math.round((t.bytes_done / t.size) * 100) : 0;
      const speed = t.speed > 0 ? formatSpeed(t.speed) : '';
      const done = formatSize(t.bytes_done);
      const size = formatSize(t.size);
      const statusClass = t.status === 'complete' ? 'complete' : t.status === 'failed' ? 'failed' : '';

      const prefix = targetList.id === 'mobileTransferList' ? 'm-' : '';
      let card = document.getElementById(`${prefix}transfer-card-${t.id}`);
      if (!card) {
        card = document.createElement('div');
        card.id = `${prefix}transfer-card-${t.id}`;
        card.className = `transfer-item ${statusClass}`;
        
        const icon = t.direction === 'send' ? '↑' : '↓';
        
        card.innerHTML = `
          <div class="transfer-icon">${icon}</div>
          <div class="transfer-info">
            <div class="transfer-name">${t.filename}</div>
            <div class="transfer-meta">
              <span class="meta-size">${size}</span>
              <span class="meta-peer">${t.direction === 'send' ? 'To' : 'From'}: ${t.peer_id.slice(-8)}</span>
              <span class="meta-speed">${speed ? speed : ''}</span>
            </div>
          </div>
          <div class="transfer-progress">
            <div class="progress-bar"><div class="progress-fill" style="width:${pct}%"></div></div>
            <div class="progress-text">${pct}% (${done})</div>
          </div>
          <div class="transfer-actions"></div>
        `;
        targetList.appendChild(card);
      }

      if (card.className !== `transfer-item ${statusClass}`) {
        card.className = `transfer-item ${statusClass}`;
      }
      
      const fill = card.querySelector('.progress-fill');
      if (fill && fill.style.width !== `${pct}%`) {
        fill.style.width = `${pct}%`;
      }
      
      const progText = card.querySelector('.progress-text');
      if (progText && progText.textContent !== `${pct}% (${done})`) {
        progText.textContent = `${pct}% (${done})`;
      }
      
      const speedSpan = card.querySelector('.meta-speed');
      if (speedSpan && speedSpan.textContent !== speed) {
        speedSpan.textContent = speed ? speed : '';
        speedSpan.style.display = speed ? 'inline-block' : 'none';
      }

      const actions = card.querySelector('.transfer-actions');
      if (actions) {
        let actionHTML = '';
        if (t.status === 'complete' && t.direction === 'receive') {
          actionHTML = `<button onclick="downloadFile('${t.id}', '${t.filename.replace(/'/g, "\\'")}')">Save</button>`;
        } else if (t.status === 'pending' || t.status === 'transferring') {
          actionHTML = `<span class="transfer-status-text">${t.status}</span>`;
        } else if (t.status === 'failed') {
          actionHTML = `<span class="transfer-status-text status-failed">${t.status}</span>`;
        }
        if (actions.innerHTML !== actionHTML) {
          actions.innerHTML = actionHTML;
        }
      }
    });

    const existingCards = targetList.querySelectorAll('.transfer-item');
    existingCards.forEach(card => {
      const prefix = targetList.id === 'mobileTransferList' ? 'm-' : '';
      const id = card.id.replace(`${prefix}transfer-card-`, '');
      if (!transfers[id]) {
        card.remove();
      }
    });
  };

  updateList(list);
  updateList(mList);
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
  return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
}

function formatSpeed(bps) {
  if (bps < 1024) return bps.toFixed(0) + ' B/s';
  if (bps < 1024 * 1024) return (bps / 1024).toFixed(1) + ' KB/s';
  return (bps / (1024 * 1024)).toFixed(1) + ' MB/s';
}

function downloadFile(id, filename) {
  const a = document.createElement('a');
  a.href = `/api/download/${id}`;
  a.download = filename || 'download';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function showToast(text, type) {
  const toast = document.getElementById('toast');
  const el = document.createElement('div');
  el.className = `toast-item ${type || ''}`;
  el.textContent = text;
  toast.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

// File sending via HTTP multipart (with progress via XHR)
function sendFile(file) {
  if (!selectedPeerId) {
    showToast('Select a peer first', 'error');
    return;
  }

  const form = new FormData();
  form.append('file', file);

  const xhr = new XMLHttpRequest();
  xhr.open('POST', `/api/send?peer_id=${encodeURIComponent(selectedPeerId)}`);

  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) {
      // Update UI for send progress (handled via WS broadcast)
    }
  };

  xhr.onload = () => {
    if (xhr.status === 200) {
      const t = JSON.parse(xhr.responseText);
      transfers[t.id] = t;
      renderTransfers();
    } else {
      showToast(`Send failed: ${xhr.responseText}`, 'error');
    }
  };

  xhr.onerror = () => showToast('Send failed: network error', 'error');
  xhr.send(form);
}

// File sending via WebSocket with chunked upload for better progress
function sendFileWS(file) {
  if (!selectedPeerId) {
    showToast('Select a peer first', 'error');
    return;
  }

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const sock = new WebSocket(`${proto}//${location.host}/ws/upload`);
  sock.binaryType = 'arraybuffer';

  sock.onopen = () => {
    // Send metadata first
    sock.send(JSON.stringify({
      type: 'upload',
      peer_id: selectedPeerId,
      filename: file.name,
      size: file.size,
    }));

    // Stream file in 64KB chunks
    const chunkSize = 64 * 1024;
    let offset = 0;

    function sendNext() {
      if (sock.bufferedAmount > 1024 * 1024) { // Buffer > 1MB, throttle backpressure
        setTimeout(sendNext, 40);
        return;
      }
      if (offset >= file.size) {
        // Signal EOF
        sock.send(JSON.stringify({ type: 'eof' }));
        return;
      }
      const slice = file.slice(offset, offset + chunkSize);
      const reader = new FileReader();
      reader.onload = (e) => {
        sock.send(e.target.result);
        offset += e.target.result.byteLength;
        sendNext();
      };
      reader.readAsArrayBuffer(slice);
    }
    sendNext();
  };

  sock.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'upload_progress' && transfers[msg.transfer_id]) {
      transfers[msg.transfer_id].bytes_done = msg.done;
      transfers[msg.transfer_id].speed = msg.speed;
      renderTransfers();
    } else if (msg.type === 'upload_complete') {
      if (msg.success) {
        showToast('Upload complete', 'success');
      } else {
        showToast(`Upload failed: ${msg.error}`, 'error');
      }
      sock.close();
    } else if (msg.type === 'transfer_new') {
      transfers[msg.transfer.id] = msg.transfer;
      renderTransfers();
    }
  };

  sock.onerror = () => {
    showToast('WebSocket upload error', 'error');
    sock.close();
  };
}

// Drop zone
const dropZone = document.getElementById('dropZone');

dropZone.addEventListener('dragover', (e) => {
  e.preventDefault();
  dropZone.classList.add('dragover');
});

dropZone.addEventListener('dragleave', () => {
  dropZone.classList.remove('dragover');
});

dropZone.addEventListener('drop', (e) => {
  e.preventDefault();
  dropZone.classList.remove('dragover');
  for (const file of e.dataTransfer.files) {
    sendFileWS(file);
  }
});

document.getElementById('pickLink').addEventListener('click', (e) => {
  e.preventDefault();
  document.getElementById('fileInput').click();
});

document.getElementById('fileInput').addEventListener('change', (e) => {
  for (const file of e.target.files) {
    sendFileWS(file);
  }
  e.target.value = '';
});

// Copy buttons
document.getElementById('copyBtn').addEventListener('click', (e) => {
  e.stopPropagation();
  const text = document.getElementById('peerId').textContent;
  navigator.clipboard.writeText(text).then(() => showToast('Peer ID copied', 'success'));
});

document.getElementById('copyAddrBtn').addEventListener('click', (e) => {
  e.stopPropagation();
  const text = document.getElementById('multiaddr').textContent;
  if (text && text !== '-') {
    navigator.clipboard.writeText(text).then(() => showToast('Multiaddr copied', 'success'));
  }
});

document.getElementById('copyConnCodeBtn').addEventListener('click', (e) => {
  e.stopPropagation();
  const text = document.getElementById('connectionCode').textContent;
  if (text && text !== '-') {
    navigator.clipboard.writeText(text).then(() => showToast('Connection Code copied', 'success'));
  }
});

document.getElementById('shortCodeCard').addEventListener('click', () => {
  const text = document.getElementById('shortCode').textContent;
  if (text && text !== '#----') {
    const digits = text.replace('#', '');
    navigator.clipboard.writeText(digits).then(() => showToast(`Quick Connect Code ${text} copied!`, 'success'));
  }
});

// Advanced Toggle panel
const toggleAdvancedBtn = document.getElementById('toggleAdvancedBtn');
const advancedPanel = document.getElementById('advancedPanel');
if (toggleAdvancedBtn && advancedPanel) {
  toggleAdvancedBtn.addEventListener('click', () => {
    const isHidden = advancedPanel.classList.toggle('hidden');
    toggleAdvancedBtn.classList.toggle('active', !isHidden);
  });
}

// Manual connect
document.getElementById('connectBtn').addEventListener('click', () => {
  const input = document.getElementById('peerAddrInput');
  const addr = input.value.trim();
  if (!addr) return;

  fetch('/api/peers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ addr }),
  })
    .then(r => {
      if (r.ok) {
        showToast('Connected!', 'success');
        input.value = '';
      } else {
        r.text().then(t => showToast(`Connect failed: ${t}`, 'error'));
      }
    })
    .catch(() => showToast('Connect failed', 'error'));
});

// Network Segment Selection & Subnet Scanning
function loadNetworkInterfaces() {
  fetch('/api/interfaces')
    .then(r => r.json())
    .then(data => {
      let interfaces = data.interfaces || [];

      // Android native bridge fallback if Go returns nothing
      if (interfaces.length === 0 && typeof Android !== 'undefined' && Android.getLocalInterfacesJson) {
        try {
          const nativeJson = Android.getLocalInterfacesJson();
          interfaces = JSON.parse(nativeJson);
        } catch (e) {
          console.error("native getLocalInterfacesJson failed", e);
        }
      }

      const panel = document.getElementById('segmentScanPanel');
      const select = document.getElementById('subnetSelect');
      const mPanel = document.getElementById('mobileSegmentScanPanel');
      const mSelect = document.getElementById('mobileSubnetSelect');

      const loadSelectOptions = (selEl, panelEl) => {
        if (!selEl || !panelEl) return;
        if (interfaces && interfaces.length > 0) {
          selEl.innerHTML = interfaces.map(ifi => {
            return `<option value="${ifi.broadcast}">${ifi.segment} (${ifi.name} - ${ifi.ip})</option>`;
          }).join('');
          panelEl.classList.remove('hidden');
        } else {
          panelEl.classList.add('hidden');
        }
      };

      loadSelectOptions(select, panel);
      loadSelectOptions(mSelect, mPanel);
    })
    .catch(() => {});
}

const scanBtn = document.getElementById('subnetScanBtn');
if (scanBtn) {
  scanBtn.addEventListener('click', () => {
    const select = document.getElementById('subnetSelect');
    const broadcast = select.value;
    if (!broadcast) return;

    scanBtn.disabled = true;
    scanBtn.classList.add('scanning');
    const textEl = document.getElementById('scanBtnText');
    const originalText = textEl.textContent;
    textEl.textContent = 'Scanning Subnet... / 正在扫描...';

    fetch('/api/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ broadcast }),
    })
      .then(r => {
        if (r.ok) {
          return r.json();
        }
        throw new Error('Scan failed');
      })
      .then(data => {
        const count = data.peers ? data.peers.length : 0;
        showToast(`Scan complete. Discovered ${count} active devices / 扫描完成，发现 ${count} 个设备`, 'success');
        if (data.peers) {
          data.peers.forEach(p => {
            peers[p.id] = p;
          });
          renderPeers();
        }
      })
      .catch(err => {
        showToast(`Scan failed / 扫描失败: ${err.message}`, 'error');
      })
      .finally(() => {
        scanBtn.disabled = false;
        scanBtn.classList.remove('scanning');
        textEl.textContent = originalText;
      });
  });
}

// Sync settings, navigation tabs, and inputs between PC and mobile layout DOMs
function initMobileController() {
  // 1. Mobile Bottom Tab Navigation Switching
  const tabs = document.querySelectorAll('.nav-tab');
  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      tabs.forEach(t => t.classList.remove('active'));
      tab.classList.add('active');

      const targetTab = tab.dataset.tab;
      document.querySelectorAll('.mobile-tab-view').forEach(view => {
        view.classList.remove('active');
      });
      const targetView = document.getElementById(`mobile-view-${targetTab}`);
      if (targetView) targetView.classList.add('active');
    });
  });

  // 2. Sync WAN settings
  const mobileWanToggleEl = document.getElementById('mobileWanToggle');
  if (mobileWanToggleEl) {
    mobileWanToggleEl.addEventListener('change', () => {
      wanToggleEl.checked = mobileWanToggleEl.checked;
      wanToggleEl.dispatchEvent(new Event('change'));
    });
  }

  // 3. Sync Auto-download settings
  const mobileAutoDownloadEl = document.getElementById('mobileAutoDownload');
  if (mobileAutoDownloadEl) {
    mobileAutoDownloadEl.addEventListener('change', () => {
      autoDownloadEl.checked = mobileAutoDownloadEl.checked;
      autoDownloadEl.dispatchEvent(new Event('change'));
    });
  }

  // 4. Sync Subnet selector and scanning triggers
  const mScanBtn = document.getElementById('mobileSubnetScanBtn');
  if (mScanBtn) {
    mScanBtn.addEventListener('click', () => {
      const select = document.getElementById('mobileSubnetSelect');
      const mainSelect = document.getElementById('subnetSelect');
      if (select && mainSelect) {
        mainSelect.value = select.value;
        
        mScanBtn.disabled = true;
        mScanBtn.classList.add('scanning');
        const textEl = document.getElementById('mobileScanBtnText');
        const originalText = textEl.textContent;
        textEl.textContent = 'Scanning... / 正在扫描...';
        
        // Trigger click event on main subnetScanBtn
        scanBtn.click();
        
        // Wait and reset the visual state once the scan finishes
        setTimeout(() => {
          mScanBtn.disabled = false;
          mScanBtn.classList.remove('scanning');
          textEl.textContent = originalText;
        }, 5000);
      }
    });
  }

  // 5. Short code click/copy trigger
  const mShortCode = document.getElementById('mobileShortCodeCard');
  if (mShortCode) {
    mShortCode.addEventListener('click', () => {
      document.getElementById('shortCodeCard').click();
    });
  }

  // 6. Mobile File Picker triggers
  const mChooseBtn = document.getElementById('mobileChooseFilesBtn');
  const mFileInput = document.getElementById('mobileFileInput');
  if (mChooseBtn && mFileInput) {
    mChooseBtn.addEventListener('click', () => {
      mFileInput.click();
    });
    mFileInput.addEventListener('change', (e) => {
      for (const file of e.target.files) {
        sendFileWS(file);
      }
      e.target.value = '';
    });
  }

  // 7. Manual Connect trigger
  const mConnectBtn = document.getElementById('mobileConnectBtn');
  if (mConnectBtn) {
    mConnectBtn.addEventListener('click', () => {
      const input = document.getElementById('mobilePeerAddrInput');
      const mainInput = document.getElementById('peerAddrInput');
      if (input && mainInput) {
        mainInput.value = input.value;
        document.getElementById('connectBtn').click();
        input.value = '';
      }
    });
  }
}

// Global initialization
initMobileController();
loadNetworkInterfaces();
loadPeersAndConnections();
connect();
