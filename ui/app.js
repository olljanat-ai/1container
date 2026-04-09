const API = location.origin;
const WS_BASE = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host;

let allContainers = [];
let environments = [];
let clusters = [];
let currentContainer = null;
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
    if (btn.dataset.tab === 'clusters') loadClusters();
  });
});

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
    const bar = document.getElementById('errors-bar');
    if (data.errors && data.errors.length > 0) {
      bar.textContent = data.errors.join(' | ');
      bar.classList.remove('hidden');
    } else {
      bar.classList.add('hidden');
    }
    renderContainers();
  } catch (e) { console.error('load containers:', e); }
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
  const empty = document.getElementById('no-containers');
  if (filtered.length === 0) { tbody.innerHTML = ''; empty.classList.remove('hidden'); return; }
  empty.classList.add('hidden');
  tbody.innerHTML = filtered.map((c, i) => `
    <tr>
      <td><strong>${esc(c.name)}</strong><br><span class="muted small">${esc(c.id)}</span></td>
      <td class="truncate">${esc(c.image)}</td>
      <td><span class="state-badge state-${c.state}">${esc(c.state)}</span></td>
      <td><span class="env-badge">${esc(c.env_name)} <span class="env-type">${esc(c.cluster_type)}</span></span></td>
      <td class="muted">${esc(c.namespace || '—')}</td>
      <td class="muted">${esc(c.node || '—')}</td>
      <td class="actions">
        <button class="btn btn-sm" data-idx="${i}" data-action="inspect">Inspect</button>
        <button class="btn btn-sm" data-idx="${i}" data-action="logs">Logs</button>
        <button class="btn btn-sm" data-idx="${i}" data-action="shell">Shell</button>
      </td>
    </tr>`).join('');
  tbody.querySelectorAll('button[data-action]').forEach(btn => {
    btn.addEventListener('click', () => {
      const c = filtered[parseInt(btn.dataset.idx)];
      openPanel(c.env_id, c.id, c.name, btn.dataset.action);
    });
  });
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
  } catch (e) { console.error('load envs:', e); }
}

function renderEnvironments() {
  const grid = document.getElementById('env-list');
  if (environments.length === 0) {
    grid.innerHTML = '<div class="empty">No environments configured. Add one to get started.</div>';
    return;
  }
  grid.innerHTML = environments.map((e, i) => `
    <div class="env-card">
      <h4><span class="status-dot ${e.online ? 'online' : 'offline'}"></span>${esc(e.name)}</h4>
      <div class="meta">Cluster: ${esc(e.cluster_name || e.cluster_id)} <span class="env-type">${esc(e.cluster_type)}</span></div>
      <div class="meta">Namespace: ${esc(e.namespace || '(none)')}</div>
      <div class="meta">Agent: ${e.online ? 'Connected' : 'Offline'}</div>
      <div class="env-actions"><button class="btn btn-sm" data-env-idx="${i}">Remove</button></div>
    </div>`).join('');
  grid.querySelectorAll('button[data-env-idx]').forEach(btn => {
    btn.addEventListener('click', () => {
      removeEnv(environments[parseInt(btn.dataset.envIdx)].id);
    });
  });
}

function populateEnvFilter() {
  const sel = document.getElementById('env-filter');
  const cur = sel.value;
  sel.innerHTML = '<option value="">All Environments</option>';
  environments.forEach(e => { sel.innerHTML += `<option value="${esc(e.id)}">${esc(e.name)}</option>`; });
  sel.value = cur;
}

// ============================================================
// Clusters
// ============================================================
async function loadClusters() {
  try {
    const resp = await fetch(`${API}/api/clusters`);
    clusters = await resp.json();
    renderClusters();
  } catch (e) { console.error('load clusters:', e); }
}

function renderClusters() {
  const grid = document.getElementById('cluster-list');
  if (clusters.length === 0) {
    grid.innerHTML = '<div class="empty">No agents connected yet.</div>';
    return;
  }
  grid.innerHTML = clusters.map(c => `
    <div class="env-card">
      <h4><span class="status-dot ${c.online ? 'online' : 'offline'}"></span>${esc(c.name)}</h4>
      <div class="meta">ID: ${esc(c.id)}</div>
      <div class="meta">Type: ${esc(c.type)}</div>
      <div class="meta">Status: ${c.online ? 'Online' : 'Offline'}</div>
    </div>`).join('');
}

// ============================================================
// Add / Remove Environment
// ============================================================
document.getElementById('add-env-btn').addEventListener('click', async () => {
  await loadClusters();
  const sel = document.getElementById('env-cluster');
  sel.innerHTML = clusters.map(c => `<option value="${esc(c.id)}" data-type="${esc(c.type)}">${esc(c.name)} (${esc(c.type)})</option>`).join('');
  if (clusters.length === 0) sel.innerHTML = '<option value="">No clusters available</option>';
  updateNsVisibility();
  document.getElementById('add-env-form').classList.remove('hidden');
});
document.getElementById('env-cluster').addEventListener('change', updateNsVisibility);

function updateNsVisibility() {
  const sel = document.getElementById('env-cluster');
  const opt = sel.options[sel.selectedIndex];
  const isDocker = opt && opt.dataset.type === 'docker-swarm';
  document.getElementById('env-ns-label').classList.toggle('hidden', isDocker);
  document.getElementById('env-ns-hint').classList.toggle('hidden', !isDocker);
}

document.getElementById('env-cancel').addEventListener('click', () => {
  document.getElementById('add-env-form').classList.add('hidden');
});
document.getElementById('env-save').addEventListener('click', async () => {
  const env = {
    name: document.getElementById('env-name').value,
    cluster_id: document.getElementById('env-cluster').value,
    namespace: document.getElementById('env-namespace').value,
  };
  if (!env.name || !env.cluster_id) return alert('Name and cluster are required');
  try {
    const resp = await fetch(`${API}/api/environments`, {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(env),
    });
    if (!resp.ok) { const e = await resp.json(); alert(e.error); return; }
    document.getElementById('add-env-form').classList.add('hidden');
    document.getElementById('env-name').value = '';
    document.getElementById('env-namespace').value = '';
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
  closeLogStream(); closeShell();
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
  closeLogStream(); closeShell();
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
    out.textContent = JSON.stringify(await resp.json(), null, 2);
  } catch (e) { out.textContent = 'Error: ' + e.message; }
}

// ============================================================
// Logs
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

function toggleLogStream() { logStreamWS ? closeLogStream() : startLogStream(); }

function startLogStream() {
  if (!currentContainer || logStreamWS) return;
  const {envID, containerID} = currentContainer;
  const tail = document.getElementById('log-tail').value || 100;
  const out = document.getElementById('log-output');
  logStreamWS = new WebSocket(`${WS_BASE}/ws/logs/${envID}/${containerID}?tail=${tail}`);
  logStreamWS.onopen = () => { updateStreamUI(true); out.textContent = ''; };
  logStreamWS.onmessage = (evt) => { out.textContent += evt.data; out.scrollTop = out.scrollHeight; };
  logStreamWS.onclose = () => { updateStreamUI(false); logStreamWS = null; };
  logStreamWS.onerror = () => { out.textContent += '\n[stream error]\n'; closeLogStream(); };
}

function closeLogStream() {
  if (logStreamWS) { logStreamWS.close(); logStreamWS = null; }
  updateStreamUI(false);
}

function updateStreamUI(live) {
  const badge = document.getElementById('log-stream-status');
  const btn = document.getElementById('log-stream-toggle');
  badge.textContent = live ? 'Live' : 'Stopped';
  badge.className = 'stream-badge ' + (live ? 'live' : 'off');
  btn.innerHTML = live ? '&#9632; Stop' : '&#9654; Stream';
  btn.classList.toggle('active', live);
}

document.getElementById('log-fetch').addEventListener('click', () => { closeLogStream(); fetchLogs(); });
document.getElementById('log-stream-toggle').addEventListener('click', toggleLogStream);

// ============================================================
// Shell
// ============================================================
function initShell() {
  if (!currentContainer) return;
  closeShell();
  const termOutput = document.getElementById('term-output');
  const termInput = document.getElementById('term-input');
  termOutput.innerHTML = ''; shellHistory = []; shellHistoryIdx = -1;
  const {envID, containerID} = currentContainer;
  shellWS = new WebSocket(`${WS_BASE}/ws/shell/${envID}/${containerID}`);
  shellWS.onopen = () => termInput.focus();
  shellWS.onmessage = (evt) => {
    try { const d = JSON.parse(evt.data); appendTermOutput(d.output, d.exit_code); }
    catch { appendTermOutput(evt.data, 0); }
    termOutput.scrollTop = termOutput.scrollHeight;
  };
  shellWS.onclose = () => { appendTermInfo('Connection closed.'); shellWS = null; };
  shellWS.onerror = () => appendTermInfo('Connection error.');
}

function closeShell() { if (shellWS) { shellWS.close(); shellWS = null; } }

function appendTermOutput(text, exitCode) {
  if (!text) return;
  const el = document.createElement('span');
  el.textContent = text + (text.endsWith('\n') ? '' : '\n');
  if (exitCode && exitCode !== 0) el.className = 'term-err';
  document.getElementById('term-output').appendChild(el);
}
function appendTermCmd(cmd) {
  const el = document.createElement('span'); el.className = 'term-cmd';
  el.textContent = '$ ' + cmd + '\n';
  document.getElementById('term-output').appendChild(el);
}
function appendTermInfo(text) {
  const el = document.createElement('span'); el.className = 'term-info';
  el.textContent = text + '\n';
  document.getElementById('term-output').appendChild(el);
}

document.getElementById('term-input').addEventListener('keydown', (e) => {
  const input = document.getElementById('term-input');
  if (e.key === 'Enter') {
    const cmd = input.value.trim();
    if (!cmd || !shellWS || shellWS.readyState !== WebSocket.OPEN) return;
    appendTermCmd(cmd);
    shellHistory.push(cmd); shellHistoryIdx = shellHistory.length;
    shellWS.send(JSON.stringify({cmd}));
    input.value = '';
  }
  if (e.key === 'ArrowUp') { e.preventDefault(); if (shellHistoryIdx > 0) input.value = shellHistory[--shellHistoryIdx]; }
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    if (shellHistoryIdx < shellHistory.length - 1) input.value = shellHistory[++shellHistoryIdx];
    else { shellHistoryIdx = shellHistory.length; input.value = ''; }
  }
});
document.getElementById('terminal').addEventListener('click', () => document.getElementById('term-input').focus());

// ============================================================
// Events
// ============================================================
document.getElementById('refresh-btn').addEventListener('click', loadContainers);
document.getElementById('search').addEventListener('input', renderContainers);
document.getElementById('state-filter').addEventListener('change', renderContainers);
document.getElementById('env-filter').addEventListener('change', loadContainers);

function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

// ============================================================
// Init
// ============================================================
(async () => {
  await loadEnvironments();
  await loadClusters();
  loadContainers();
  setInterval(loadContainers, 30000);
})();
