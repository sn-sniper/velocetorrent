const ENDPOINT = 'http://127.0.0.1:7878';

const sourceEl = document.getElementById('source');
const dirSelectEl = document.getElementById('dirSelect');
const customDirEl = document.getElementById('customDir');
const tokenEl = document.getElementById('token');
const telemetryEl = document.getElementById('telemetry');
const statusEl = document.getElementById('status');
const sendBtn = document.getElementById('sendBtn');

function headers(token) {
  const h = { 'Content-Type': 'application/json' };
  if (token) {
    h['Authorization'] = `Bearer ${token}`;
    h['X-VT-Token'] = token;
  }
  return h;
}

async function init() {
  const saved = await chrome.storage.local.get(['vtToken', 'vtOutputDir']);
  if (saved.vtToken) tokenEl.value = saved.vtToken;
  if (saved.vtOutputDir) customDirEl.value = saved.vtOutputDir;

  try {
    const res = await fetch(`${ENDPOINT}/api/state`, {
      headers: headers(tokenEl.value.trim()),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const state = await res.json();
    const downMB = ((state.globalDownBytesPs || 0) / (1024 * 1024)).toFixed(1);
    telemetryEl.textContent = `DOWN ${downMB} MB/s | ${state.torrents?.length || 0} ACTIVE`;

    dirSelectEl.innerHTML = '';
    for (const d of state.savedDirs || []) {
      const opt = document.createElement('option');
      opt.value = d;
      opt.textContent = d;
      dirSelectEl.appendChild(opt);
    }
    if (!customDirEl.value && state.defaultOutputDir) {
      customDirEl.value = state.defaultOutputDir;
    }
  } catch (err) {
    telemetryEl.textContent = 'DAEMON OFFLINE';
  }
}

dirSelectEl.addEventListener('change', () => {
  customDirEl.value = dirSelectEl.value;
});

sendBtn.addEventListener('click', async () => {
  const source = sourceEl.value.trim();
  const outputDir = customDirEl.value.trim();
  const token = tokenEl.value.trim();

  await chrome.storage.local.set({ vtToken: token, vtOutputDir: outputDir });

  if (!source) {
    statusEl.textContent = 'ERROR: ENTER MAGNET:? OR .TORRENT SOURCE';
    return;
  }

  try {
    const res = await fetch(`${ENDPOINT}/api/torrents`, {
      method: 'POST',
      headers: headers(token),
      body: JSON.stringify({ source, outputDir, sequential: false }),
    });
    const data = await res.json();
    if (!res.ok) {
      statusEl.textContent = `ERROR: ${data.error || res.statusText}`;
      return;
    }
    statusEl.textContent = `QUEUED: ${data.name} (${data.status})`;
    sourceEl.value = '';
    void init();
  } catch (err) {
    statusEl.textContent = `ERROR: ${err.message}`;
  }
});

void init();
