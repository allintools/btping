import './style.css';

import { Activity, createIcons, Download, Eraser, Play, Radar, Search, Square } from 'lucide';

import { ExportCSV, StartScan, StopScan } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

const $ = (id) => document.getElementById(id);

createIcons({
  icons: { Activity, Download, Eraser, Play, Radar, Search, Square },
});

const STATUS_META = {
  pending: { label: '等待中', cls: 'pending', rank: 6 },
  resolving: { label: '解析中', cls: 'pending', rank: 5 },
  open: { label: '开放', cls: 'open', rank: 0 },
  refused: { label: '关闭', cls: 'refused', rank: 1 },
  timeout: { label: '超时', cls: 'timeout', rank: 2 },
  error: { label: '错误', cls: 'error', rank: 3 },
  unresolved: { label: '解析失败', cls: 'error', rank: 4 },
};

const state = {
  rows: new Map(),
  order: [],
  els: new Map(),
  sort: { key: null, dir: 1 },
  filter: '',
  onlyFailed: false,
  running: false,
};

const inputs = ['targets', 'ports', 'timeout', 'rounds', 'interval', 'concurrency'].map($);

function statusMeta(status) {
  return STATUS_META[status] || { label: status || '—', cls: 'pending', rank: 7 };
}

function fmtMs(value) {
  if (!value) return '—';
  if (value < 0.01) return '<0.01';
  return value < 10 ? value.toFixed(2) : value.toFixed(1);
}

function fmtDuration(ms) {
  if (!ms && ms !== 0) return '—';
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  const minutes = Math.floor(ms / 60000);
  const seconds = Math.round((ms % 60000) / 1000);
  return `${minutes} 分 ${seconds} 秒`;
}

function rowVisible(row) {
  if (state.onlyFailed && row.status === 'open') return false;
  if (!state.filter) return true;
  const query = state.filter.toLowerCase();
  return (
    row.host.toLowerCase().includes(query) ||
    String(row.port).includes(query) ||
    (row.ip || '').toLowerCase().includes(query) ||
    statusMeta(row.status).label.includes(query) ||
    (row.error || '').toLowerCase().includes(query)
  );
}

function cell(className) {
  const td = document.createElement('td');
  if (className) td.className = className;
  return td;
}

function createRowElement() {
  const tr = document.createElement('tr');
  tr.className = 'row';

  const hostCell = cell('col-host');
  const host = document.createElement('span');
  host.className = 'host';
  hostCell.append(host);

  const port = cell('num col-port mono');
  const ip = cell('col-ip mono');

  const statusCell = cell('col-status');
  const tag = document.createElement('span');
  tag.className = 'status-tag';
  const dot = document.createElement('span');
  dot.className = 'dot';
  const statusLabel = document.createElement('span');
  statusLabel.className = 'label';
  tag.append(dot, statusLabel);
  statusCell.append(tag);

  const last = cell('num mono');
  const min = cell('num mono');
  const avg = cell('num mono');
  const max = cell('num mono');
  const success = cell('num mono');

  const lossCell = cell('num');
  const loss = document.createElement('span');
  loss.className = 'loss';
  const bar = document.createElement('span');
  bar.className = 'loss-bar';
  const lossFill = document.createElement('i');
  bar.append(lossFill);
  const lossText = document.createElement('span');
  lossText.className = 'mono';
  loss.append(bar, lossText);
  lossCell.append(loss);

  const note = cell('col-note');

  tr.append(hostCell, port, ip, statusCell, last, min, avg, max, success, lossCell, note);
  tr.cellsRef = { host, port, ip, tag, statusLabel, last, min, avg, max, success, lossFill, lossText, note };
  return tr;
}

function updateRowElement(el, row) {
  const refs = el.cellsRef;
  refs.host.textContent = row.host;
  refs.port.textContent = row.port;
  refs.ip.textContent = row.ip || '—';

  const meta = statusMeta(row.status);
  refs.tag.className = `status-tag ${meta.cls}`;
  refs.statusLabel.textContent = meta.label;

  refs.last.textContent = fmtMs(row.lastMs);
  refs.min.textContent = fmtMs(row.minMs);
  refs.avg.textContent = fmtMs(row.avgMs);
  refs.max.textContent = fmtMs(row.maxMs);
  refs.success.textContent = row.total ? `${row.success}/${row.total}` : '—';
  refs.lossText.textContent = row.total ? `${row.loss}%` : '—';
  refs.lossFill.style.width = `${Math.max(0, Math.min(100, row.loss || 0))}%`;
  refs.note.textContent = row.error || '—';
  refs.note.title = row.error || '';

  el.className = `row status-${meta.cls}${row.loss > 0 ? ' lossy' : ''}${rowVisible(row) ? '' : ' hidden'}`;
}

function upsertRow(row) {
  let el = state.els.get(row.key);
  if (!el) {
    el = createRowElement();
    el.dataset.key = row.key;
    state.els.set(row.key, el);
    state.order.push(row.key);
    $('rows').append(el);
  }
  state.rows.set(row.key, row);
  updateRowElement(el, row);
}

function applyFilter() {
  let visible = 0;
  for (const [key, row] of state.rows) {
    const el = state.els.get(key);
    if (!el) continue;
    const show = rowVisible(row);
    el.classList.toggle('hidden', !show);
    if (show) visible += 1;
  }
  $('empty').classList.toggle('hidden', visible > 0);
}

function statusRank(status) {
  return statusMeta(status).rank;
}

function compareRows(a, b, key) {
  switch (key) {
    case 'host':
      return a.host.localeCompare(b.host, 'zh-Hans-CN') || a.port - b.port;
    case 'ip':
      return (a.ip || '').localeCompare(b.ip || '');
    case 'status':
      return statusRank(a.status) - statusRank(b.status) || a.host.localeCompare(b.host, 'zh-Hans-CN');
    case 'port':
      return a.port - b.port || a.host.localeCompare(b.host, 'zh-Hans-CN');
    default:
      return (a[key] || 0) - (b[key] || 0);
  }
}

function sortRows() {
  const { key, dir } = state.sort;
  if (!key) return;
  state.order.sort((left, right) => compareRows(state.rows.get(left), state.rows.get(right), key) * dir);
  const fragment = document.createDocumentFragment();
  for (const key_ of state.order) {
    const el = state.els.get(key_);
    if (el) fragment.append(el);
  }
  $('rows').append(fragment);
}

function updateSortIndicators() {
  document.querySelectorAll('th[data-sort]').forEach((th) => {
    th.classList.toggle('sorted', th.dataset.sort === state.sort.key);
    th.dataset.dir = th.dataset.sort === state.sort.key ? (state.sort.dir === 1 ? 'asc' : 'desc') : '';
  });
}

function liveTotals() {
  let reachable = 0;
  let success = 0;
  let failed = 0;
  let sum = 0;
  let count = 0;
  for (const row of state.rows.values()) {
    if (row.success > 0) reachable += 1;
    success += row.success;
    failed += Math.max(0, row.total - row.success);
    if (row.success > 0) {
      sum += row.avgMs * row.success;
      count += row.success;
    }
  }
  return { reachable, success, failed, avg: count ? sum / count : 0 };
}

function renderStats({ targets, reachable, success, failed, avg, elapsedMs, avgText, elapsedText }) {
  $('stat-targets').textContent = targets;
  $('stat-reachable').textContent = reachable;
  $('stat-success').textContent = success;
  $('stat-failed').textContent = failed;
  $('stat-avg').textContent = avgText !== undefined ? avgText : (avg ? fmtMs(avg) : '—');
  $('stat-elapsed').textContent = elapsedText !== undefined ? elapsedText : fmtDuration(elapsedMs);
}

function setStatus(kind, text) {
  $('status').className = `status status-${kind}`;
  $('status-text').textContent = text;
}

function setRunning(running) {
  state.running = running;
  $('start').disabled = running;
  $('stop').disabled = !running;
}

let toastTimer = 0;
function showToast(message, kind) {
  const toast = $('toast');
  toast.textContent = message;
  toast.className = `toast show${kind === 'error' ? ' error' : ''}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    toast.className = 'toast';
  }, 4200);
}

function resetResults() {
  state.rows.clear();
  state.els.clear();
  state.order = [];
  $('rows').innerHTML = '';
  $('progress-bar').style.width = '0%';
  renderStats({ targets: 0, reachable: 0, success: 0, failed: 0, avgText: '—', elapsedText: '—' });
  applyFilter();
}

function readInputs() {
  return {
    targets: $('targets').value,
    ports: $('ports').value,
    timeoutMs: Number($('timeout').value) || 0,
    concurrency: Number($('concurrency').value) || 0,
    rounds: Number($('rounds').value) || 0,
    intervalMs: Number($('interval').value),
  };
}

const STORAGE_KEY = 'btping.inputs';

function saveInputs() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(readInputs()));
  } catch (err) {
    console.warn('无法保存输入', err);
  }
}

function loadInputs() {
  try {
    const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || 'null');
    if (!saved) return;
    $('targets').value = saved.targets || '';
    $('ports').value = saved.ports || '80,443';
    if (saved.timeoutMs) $('timeout').value = saved.timeoutMs;
    if (saved.concurrency) $('concurrency').value = saved.concurrency;
    if (saved.rounds) $('rounds').value = saved.rounds;
    if (saved.intervalMs !== undefined && saved.intervalMs !== null) $('interval').value = saved.intervalMs;
  } catch (err) {
    console.warn('无法读取上次输入', err);
  }
}

async function startRun() {
  if (state.running) return;
  const request = readInputs();
  saveInputs();

  const backup = Array.from(state.rows.values());
  resetResults();
  setRunning(true);
  setStatus('running', '检测中');
  $('progress-bar').style.width = '0%';

  try {
    const started = await StartScan(request);
    $('stat-targets').textContent = started.targets;
    if (started.warnings && started.warnings.length) {
      showToast(started.warnings.join('；'));
    }
  } catch (err) {
    setRunning(false);
    setStatus('error', '失败');
    backup.forEach(upsertRow);
    applyFilter();
    showToast(err && err.message ? err.message : String(err), 'error');
  }
}

async function stopRun() {
  if (!state.running) return;
  setStatus('running', '正在停止');
  try {
    await StopScan();
  } catch (err) {
    console.error(err);
  }
}

async function exportResults() {
  try {
    const path = await ExportCSV();
    if (path) showToast(`已导出到 ${path}`);
  } catch (err) {
    showToast(err && err.message ? err.message : String(err), 'error');
  }
}

function bindEvents() {
  $('start').addEventListener('click', startRun);
  $('stop').addEventListener('click', stopRun);
  $('export').addEventListener('click', exportResults);
  $('clear').addEventListener('click', () => {
    resetResults();
    setStatus('idle', '就绪');
  });

  document.querySelectorAll('th[data-sort]').forEach((th) => {
    th.addEventListener('click', () => {
      const key = th.dataset.sort;
      state.sort = state.sort.key === key ? { key, dir: -state.sort.dir } : { key, dir: 1 };
      updateSortIndicators();
      sortRows();
    });
  });

  document.querySelectorAll('.chip').forEach((chip) => {
    chip.addEventListener('click', () => {
      $('ports').value = chip.dataset.ports;
      saveInputs();
    });
  });

  $('filter').addEventListener('input', (event) => {
    state.filter = event.target.value.trim();
    applyFilter();
  });
  $('only-failed').addEventListener('change', (event) => {
    state.onlyFailed = event.target.checked;
    applyFilter();
  });

  inputs.forEach((input) => input.addEventListener('change', saveInputs));

  window.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      startRun();
    }
    if (event.key === 'Escape' && state.running) {
      stopRun();
    }
  });
}

function bindBackendEvents() {
  EventsOn('row', (row) => {
    upsertRow(row);
    if (state.sort.key) sortRows();
    applyFilter();
  });

  EventsOn('progress', (progress) => {
    const totals = liveTotals();
    const percent = progress.planned ? Math.min(100, (progress.executed / progress.planned) * 100) : 0;
    $('progress-bar').style.width = `${percent}%`;
    renderStats({
      targets: state.rows.size,
      reachable: totals.reachable,
      success: totals.success,
      failed: totals.failed,
      avg: totals.avg,
      elapsedMs: progress.elapsedMs,
    });
    if (state.running) setStatus('running', `检测中 ${Math.round(percent)}%`);
  });

  EventsOn('done', (payload) => {
    setRunning(false);
    const rows = (payload && payload.rows) || [];
    rows.forEach(upsertRow);
    if (state.sort.key) sortRows();
    applyFilter();

    const summary = (payload && payload.summary) || {};
    renderStats({
      targets: summary.targets || state.rows.size,
      reachable: summary.reachable || 0,
      success: summary.success || 0,
      failed: summary.failed || 0,
      avgText: summary.avgMs ? fmtMs(summary.avgMs) : '—',
      elapsedText: fmtDuration(summary.elapsedMs),
    });
    $('progress-bar').style.width = '100%';
    setStatus(summary.cancelled ? 'idle' : 'done', summary.cancelled ? '已停止' : '已完成');
  });
}

loadInputs();
bindEvents();
bindBackendEvents();
$('targets').focus();
setStatus('idle', '就绪');
