// Container Hub UI
const API = location.origin;
const WS_BASE = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host;

let allContainers = [];
let environments = [];
let currentContainer = null; // {envID, containerID, name}

// Active WebSocket connections
let logStreamWS = null;
let shellWS = null;
let shellHistory = [];
let shellHistoryIdx = -1;

// --- Tabs ---
document.querySelectorAll('.tab').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById(btn.dataset.tab).classList.add('active');
    if (btn.dataset.tab === 'environments') loadEnvironments();
  });
});

// --- Side panel tabs ---
document.querySelectorAll('.side-tab').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.side-tab').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.side-body').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('side-' + btn.dataset.side).classList.add('active');
    if (btn.dataset.side === 'logs' && currentContainer) fetchLogs();
    if (btn.dataset.side === 'inspect' && currentContainer) fetchInspect();
    if (btn.dataset.side === 'shell' && currentContainer) initShell();
  });
});

// ============================================================
// Containers
// ============================================================

async function loadContainers() {
  const envFilter = document.getElementById('env-filter').value;
  const url = envFilter ? `${API}/api/containers?env=${envFilter}` : `${API}/api/containers`;
  try {
    const resp = await fetch(url);
    const data = await resp.json();
    allContainers = data.containers || [];
    if (data.errors && data.errors.length > 0) {
      const bar = document.getElementById('errors-bar');
      bar.textContent = data.errors.join(' | ');
      bar.classList.remove('hidden');
    } else {
      document.getElementById('errors-bar').classList.add('hidden');
    }
    renderContainers();
  } catch (e) {
    console.error('Failed to load containers:', e);
  }
}

function renderContainers() {
  const search = document.getElementById('search').value.toLowerCase();
  const stateFilter = document.getElementById('state-filter').value;

  let filtered = allContainers.filter(c => {
    if (search && !c.name.toLowerCase().includes(search) &&
        !c.image.toLowerCase().includes(search) &&
        !c.id.toLowerCase().includes(search)) return false;
    if (stateFilter && c.state !== stateFilter) return false;
    return true;
  });

  const tbody = document.getElementById('container-body');
  const noContainers = document.getElementById('no-containers');

  if (filtered.length === 0) {
    tbody.innerHTML = '';
    noContainers.classList.remove('hidden');
    return;
  }
  noContainers.classList.add('hidden');

  tbody.innerHTML = filtered.map(c => `
    <tr>
      <td><strong>${esc(c.name)}</strong><br><span style="color:var(--muted);font-size:11px">${esc(c.id)}</span></td>
      <td style="max-width:220px;overflow:hidden;text-overflow:ellipsis">${esc(c.image)}</td>
      <td><span class="state-badge state-${c.state}">${esc(c.state)}</span></td>
      <td style="color:var(--muted)">${esc(c.status)}</td>
      <td><span class="env-badge">${esc(c.env_name)} <span class="env-type">${esc(c.env_type)}</span></span></td>
      <td style="color:var(--muted)">${esc(c.node || '—')}</td>
      <td class="actions">
        <button class="btn btn-sm" onclick="openPanel('${esc(c.env_id)}','${esc(c.id)}','${esc(c.name)}','inspect')">Inspect</button>
        <button class="btn btn-sm" onclick="openPanel('${esc(c.env_id)}','${esc(c.id)}','${esc(c.name)}','logs')">Logs</button>
        <button class="btn btn-sm" onclick="openPanel('${esc(c.env_id)}','${esc(c.id)}','${esc(c.name)}','shell')">Shell</button>
      </td>
    </tr>
  `).join('');
}

// ============================================================
// Environments
// ============================================================

async function loadEnvironments() {
  try {
    const resp = await fetch(`${API}/api/environments`);
    environments = await resp.json();
    renderEnvironments();
    populateEnvFilter();
  } catch (e) {
    console.error('Failed to load environments:', e);
  }
}

function renderEnvironments() {
  const grid = document.getElementById('env-list');
  if (environments.length === 0) {
    grid.innerHTML = '<div class="empty">No environments configured. Add one to get started.</div>';
    return;
  }
  grid.innerHTML = environments.map(e => `
    <div class="env-card">
      <h4><span class="status-dot ${e.online ? 'online' : 'offline'}"></span>${esc(e.name)}</h4>
      <div class="meta">Type: ${esc(e.type)}</div>
      <div class="meta">Endpoint: ${esc(e.endpoint || '(via tunnel)')}</div>
      <div class="meta">${e.tunnel ? 'Tunnel: ' + (e.online ? 'Connected' : 'Disconnected') : 'Direct connection'}</div>
      <div class="env-actions"><button class="btn btn-sm" onclick="removeEnv('${esc(e.id)}')">Remove</button></div>
    </div>
  `).join('');
}

function populateEnvFilter() {
  const select = document.getElementById('env-filter');
  const current = select.value;
  select.innerHTML = '<option value="">All Environments</option>';
  environments.forEach(e => {
    select.innerHTML += `<option value="${esc(e.id)}">${esc(e.name)}</option>`;
  });
  select.value = current;
}

document.getElementById('add-env-btn').addEventListener('click', () => {
  document.getElementById('add-env-form').classList.remove('hidden');
});
document.getElementById('env-cancel').addEventListener('click', () => {
  document.getElementById('add-env-form').classList.add('hidden');
});
document.getElementById('env-save').addEventListener('click', async () => {
  const env = {
    name: document.getElementById('env-name').value,
    type: document.getElementById('env-type').value,
    endpoint: document.getElementById('env-endpoint').value,
    token: document.getElementById('env-token').value,
    tunnel: document.getElementById('env-tunnel').checked,
    skip_tls: document.getElementById('env-skip-tls').checked,
  };
  if (!env.name) return alert('Name is required');
  try {
    await fetch(`${API}/api/environments`, {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(env),
    });
    document.getElementById('add-env-form').classList.add('hidden');
    ['env-name','env-endpoint','env-token'].forEach(id => document.getElementById(id).value = '');
    loadEnvironments();
  } catch (e) { alert('Failed: ' + e.message); }
});

async function removeEnv(id) {
  if (!confirm('Remove this environment?')) return;
  await fetch(`${API}/api/environments/${id}`, {method: 'DELETE'});
  loadEnvironments();
}

// ============================================================
// Side Panel
// ============================================================

function openPanel(envID, containerID, name, tab) {
  // Close previous connections
  closeLogStream();
  closeShell();

  currentContainer = {envID, containerID, name};
  document.getElementById('side-title').textContent = name;
  document.getElementById('side-panel').classList.remove('hidden');

  document.querySelectorAll('.side-tab').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.side-body').forEach(b => b.classList.remove('active'));
  document.querySelector(`.side-tab[data-side="${tab}"]`).classList.add('active');
  document.getElementById('side-' + tab).classList.add('active');

  if (tab === 'inspect') fetchInspect();
  if (tab === 'logs') fetchLogs();
  if (tab === 'shell') initShell();
}

document.getElementById('side-close').addEventListener('click', () => {
  document.getElementById('side-panel').classList.add('hidden');
  closeLogStream();
  closeShell();
  currentContainer = null;
});

// ============================================================
// Inspect
// ============================================================

async function fetchInspect() {
  if (!currentContainer) return;
  const out = document.getElementById('inspect-output');
  out.textContent = 'Loading…';
  try {
    const {envID, containerID} = currentContainer;
    const resp = await fetch(`${API}/api/containers/${envID}/${containerID}`);
    const data = await resp.json();
    out.textContent = JSON.stringify(data, null, 2);
  } catch (e) { out.textContent = 'Error: ' + e.message; }
}

// ============================================================
// Logs (fetch + stream)
// ============================================================

async function fetchLogs() {
  if (!currentContainer) return;
  const out = document.getElementById('log-output');
  const tail = document.getElementById('log-tail').value || 200;
  out.textContent = 'Loading…';
  try {
    const {envID, containerID} = currentContainer;
    const resp = await fetch(`${API}/api/containers/${envID}/${containerID}/logs?tail=${tail}`);
    out.textContent = await resp.text();
    out.scrollTop = out.scrollHeight;
  } catch (e) { out.textContent = 'Error: ' + e.message; }
}

function toggleLogStream() {
  if (logStreamWS) {
    closeLogStream();
  } else {
    startLogStream();
  }
}

function startLogStream() {
  if (!currentContainer || logStreamWS) return;
  const {envID, containerID} = currentContainer;
  const tail = document.getElementById('log-tail').value || 100;
  const out = document.getElementById('log-output');

  logStreamWS = new WebSocket(`${WS_BASE}/ws/logs/${envID}/${containerID}?tail=${tail}`);

  logStreamWS.onopen = () => {
    updateStreamUI(true);
    out.textContent = ''; // Clear for streaming
  };

  logStreamWS.onmessage = (evt) => {
    out.textContent += evt.data;
    // Auto-scroll to bottom
    out.scrollTop = out.scrollHeight;
  };

  logStreamWS.onclose = () => {
    updateStreamUI(false);
    logStreamWS = null;
  };

  logStreamWS.onerror = () => {
    out.textContent += '\n[stream error]\n';
    closeLogStream();
  };
}

function closeLogStream() {
  if (logStreamWS) {
    logStreamWS.close();
    logStreamWS = null;
  }
  updateStreamUI(false);
}

function updateStreamUI(live) {
  const badge = document.getElementById('log-stream-status');
  const btn = document.getElementById('log-stream-toggle');
  if (live) {
    badge.textContent = 'Live';
    badge.className = 'stream-badge live';
    btn.innerHTML = '&#9632; Stop';
    btn.classList.add('active');
  } else {
    badge.textContent = 'Stopped';
    badge.className = 'stream-badge off';
    btn.innerHTML = '&#9654; Stream';
    btn.classList.remove('active');
  }
}

document.getElementById('log-fetch').addEventListener('click', () => {
  closeLogStream();
  fetchLogs();
});
document.getElementById('log-stream-toggle').addEventListener('click', toggleLogStream);

// ============================================================
// Shell (WebSocket-based interactive)
// ============================================================

function initShell() {
  if (!currentContainer) return;
  closeShell();

  const termOutput = document.getElementById('term-output');
  const termInput = document.getElementById('term-input');
  termOutput.innerHTML = '';
  shellHistory = [];
  shellHistoryIdx = -1;

  const {envID, containerID, name} = currentContainer;
  shellWS = new WebSocket(`${WS_BASE}/ws/shell/${envID}/${containerID}`);

  shellWS.onopen = () => {
    termInput.focus();
  };

  shellWS.onmessage = (evt) => {
    try {
      const data = JSON.parse(evt.data);
      if (data.type === 'output') {
        appendTermOutput(data.output, data.exit_code);
      }
    } catch (e) {
      appendTermOutput(evt.data, 0);
    }
    termOutput.scrollTop = termOutput.scrollHeight;
  };

  shellWS.onclose = () => {
    appendTermInfo('Connection closed.');
    shellWS = null;
  };

  shellWS.onerror = () => {
    appendTermInfo('Connection error.');
  };
}

function closeShell() {
  if (shellWS) {
    shellWS.close();
    shellWS = null;
  }
}

function appendTermOutput(text, exitCode) {
  const termOutput = document.getElementById('term-output');
  if (!text) return;
  const span = document.createElement('span');
  span.textContent = text;
  if (exitCode && exitCode !== 0) {
    span.className = 'term-err';
  }
  termOutput.appendChild(span);
  // Add newline if needed
  if (!text.endsWith('\n')) {
    termOutput.appendChild(document.createTextNode('\n'));
  }
}

function appendTermCmd(cmd) {
  const termOutput = document.getElementById('term-output');
  const line = document.createElement('span');
  line.className = 'term-cmd';
  line.textContent = '$ ' + cmd + '\n';
  termOutput.appendChild(line);
}

function appendTermInfo(text) {
  const termOutput = document.getElementById('term-output');
  const span = document.createElement('span');
  span.className = 'term-info';
  span.textContent = text + '\n';
  termOutput.appendChild(span);
}

document.getElementById('term-input').addEventListener('keydown', (e) => {
  const input = document.getElementById('term-input');

  if (e.key === 'Enter') {
    const cmd = input.value.trim();
    if (!cmd || !shellWS || shellWS.readyState !== WebSocket.OPEN) return;

    appendTermCmd(cmd);
    shellHistory.push(cmd);
    shellHistoryIdx = shellHistory.length;

    shellWS.send(JSON.stringify({cmd}));
    input.value = '';
  }

  // Command history: up/down arrows
  if (e.key === 'ArrowUp') {
    e.preventDefault();
    if (shellHistoryIdx > 0) {
      shellHistoryIdx--;
      input.value = shellHistory[shellHistoryIdx];
    }
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    if (shellHistoryIdx < shellHistory.length - 1) {
      shellHistoryIdx++;
      input.value = shellHistory[shellHistoryIdx];
    } else {
      shellHistoryIdx = shellHistory.length;
      input.value = '';
    }
  }
});

// Focus terminal input when clicking anywhere in the terminal
document.getElementById('terminal').addEventListener('click', () => {
  document.getElementById('term-input').focus();
});

// ============================================================
// Refresh & Filters
// ============================================================

document.getElementById('refresh-btn').addEventListener('click', loadContainers);
document.getElementById('search').addEventListener('input', renderContainers);
document.getElementById('state-filter').addEventListener('change', renderContainers);
document.getElementById('env-filter').addEventListener('change', loadContainers);

// ============================================================
// Helpers
// ============================================================

function esc(str) {
  if (!str) return '';
  const d = document.createElement('div');
  d.textContent = str;
  return d.innerHTML;
}

// ============================================================
// Init
// ============================================================

(async function init() {
  await loadEnvironments();
  loadContainers();
  setInterval(loadContainers, 30000);
})();
