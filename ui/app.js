// Container Hub UI
const API = location.origin;
let allContainers = [];
let environments = [];
let currentContainer = null; // {envID, containerID}

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
  });
});

// --- Load Containers ---
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
        <button class="btn btn-sm" onclick="openPanel('${esc(c.env_id)}','${esc(c.id)}','${esc(c.name)}','exec')">Exec</button>
      </td>
    </tr>
  `).join('');
}

// --- Load Environments ---
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
      <h4>
        <span class="status-dot ${e.online ? 'online' : 'offline'}"></span>
        ${esc(e.name)}
      </h4>
      <div class="meta">Type: ${esc(e.type)}</div>
      <div class="meta">Endpoint: ${esc(e.endpoint || '(via tunnel)')}</div>
      <div class="meta">${e.tunnel ? 'Tunnel: ' + (e.online ? 'Connected' : 'Disconnected') : 'Direct connection'}</div>
      <div class="env-actions">
        <button class="btn btn-sm" onclick="removeEnv('${esc(e.id)}')">Remove</button>
      </div>
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

// --- Add Environment ---
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
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(env),
    });
    document.getElementById('add-env-form').classList.add('hidden');
    document.getElementById('env-name').value = '';
    document.getElementById('env-endpoint').value = '';
    document.getElementById('env-token').value = '';
    loadEnvironments();
  } catch (e) {
    alert('Failed to add environment: ' + e.message);
  }
});

async function removeEnv(id) {
  if (!confirm('Remove this environment?')) return;
  await fetch(`${API}/api/environments/${id}`, {method: 'DELETE'});
  loadEnvironments();
}

// --- Side Panel ---
function openPanel(envID, containerID, name, tab) {
  currentContainer = {envID, containerID};
  document.getElementById('side-title').textContent = name;
  document.getElementById('side-panel').classList.remove('hidden');

  // Activate the requested tab
  document.querySelectorAll('.side-tab').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.side-body').forEach(b => b.classList.remove('active'));
  document.querySelector(`.side-tab[data-side="${tab}"]`).classList.add('active');
  document.getElementById('side-' + tab).classList.add('active');

  if (tab === 'inspect') fetchInspect();
  if (tab === 'logs') fetchLogs();
  if (tab === 'exec') {
    document.getElementById('exec-output').textContent = '';
    document.getElementById('exec-cmd').value = '';
  }
}

document.getElementById('side-close').addEventListener('click', () => {
  document.getElementById('side-panel').classList.add('hidden');
  currentContainer = null;
});

async function fetchInspect() {
  if (!currentContainer) return;
  const out = document.getElementById('inspect-output');
  out.textContent = 'Loading…';
  try {
    const {envID, containerID} = currentContainer;
    const resp = await fetch(`${API}/api/containers/${envID}/${containerID}`);
    const data = await resp.json();
    out.textContent = JSON.stringify(data, null, 2);
  } catch (e) {
    out.textContent = 'Error: ' + e.message;
  }
}

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
  } catch (e) {
    out.textContent = 'Error: ' + e.message;
  }
}

document.getElementById('log-refresh').addEventListener('click', fetchLogs);

// --- Exec ---
document.getElementById('exec-run').addEventListener('click', runExec);
document.getElementById('exec-cmd').addEventListener('keydown', e => {
  if (e.key === 'Enter') runExec();
});

async function runExec() {
  if (!currentContainer) return;
  const cmdStr = document.getElementById('exec-cmd').value.trim();
  if (!cmdStr) return;
  const out = document.getElementById('exec-output');
  out.textContent = '$ ' + cmdStr + '\n\nRunning…';

  // Split command simply by spaces (good enough for most troubleshooting)
  const cmd = ['sh', '-c', cmdStr];

  try {
    const {envID, containerID} = currentContainer;
    const resp = await fetch(`${API}/api/containers/${envID}/${containerID}/exec`, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({cmd}),
    });
    const data = await resp.json();
    if (data.error) {
      out.textContent = '$ ' + cmdStr + '\n\nError: ' + data.error;
    } else {
      out.textContent = '$ ' + cmdStr + '\n\n' + (data.output || '(no output)') +
        (data.exit_code !== 0 ? '\n\n[exit code: ' + data.exit_code + ']' : '');
    }
  } catch (e) {
    out.textContent = '$ ' + cmdStr + '\n\nError: ' + e.message;
  }
}

// --- Refresh / Filters ---
document.getElementById('refresh-btn').addEventListener('click', loadContainers);
document.getElementById('search').addEventListener('input', renderContainers);
document.getElementById('state-filter').addEventListener('change', renderContainers);
document.getElementById('env-filter').addEventListener('change', loadContainers);

// --- Escape HTML ---
function esc(str) {
  if (!str) return '';
  const d = document.createElement('div');
  d.textContent = str;
  return d.innerHTML;
}

// --- Init ---
(async function init() {
  await loadEnvironments();
  loadContainers();
  // Auto-refresh every 30s
  setInterval(loadContainers, 30000);
})();
