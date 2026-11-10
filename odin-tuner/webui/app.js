(() => {
  'use strict';

  const TOKEN = window.VOLT_FORGE_TOKEN || '';
  const TOKEN_SUFFIX = TOKEN ? `?token=${encodeURIComponent(TOKEN)}` : '';
  const STREAM_URL = `/stream${TOKEN_SUFFIX}`;
  const MAX_SAMPLES = 300;
  const MAX_LOGS = 100;
  const MIN_RENDER_INTERVAL = 1000 / 30;
  const RANGE_LABELS = new Map([[30000, '30 SEC'], [60000, '60 SEC'], [300000, '5 MIN']]);
  const PROFILE_LABELS = {
    system: 'SYSTEM RESTORE',
    performance: 'PERFORMANCE',
    benchmark: 'BENCHMARK',
    safe: 'SAFE / COOLING',
    manual: 'MANUAL'
  };
  const COLORS = {
    cpu: '#58d68d',
    mid: '#79aef8',
    prime: '#f0b45c',
    gpu: '#b88cff',
    battery: '#ff728f',
    board: '#55d8e8',
    temp: '#ffb454'
  };

  class RingBuffer {
    constructor(capacity) {
      this.capacity = capacity;
      this.items = [];
      this.start = 0;
    }

    push(value) {
      if (this.items.length < this.capacity) {
        this.items.push(value);
      } else {
        this.items[this.start] = value;
        this.start = (this.start + 1) % this.capacity;
      }
    }

    values() {
      if (this.items.length < this.capacity || this.start === 0) return this.items.slice();
      return this.items.slice(this.start).concat(this.items.slice(0, this.start));
    }

    latest() {
      const values = this.values();
      return values.length ? values[values.length - 1] : null;
    }
  }

  const Store = {
    samples: new RingBuffer(MAX_SAMPLES),
    snapshot: null,
    controls: [],
    controlsSignature: '',
    pending: new Map(),
    logs: [],
    page: 'overview',
    range: 60000,
    source: null,
    reconnectTimer: null,
    reconnectDelay: 1000,
    reconnectCount: 0,
    lastPacketAt: 0,
    lastSampleAt: 0,
    firstDataAt: 0,
    packets: 0,
    bytes: 0,
    settingsInitialized: false,
    charts: [],
    controlMountSignatures: new Map()
  };

  const $ = selector => document.querySelector(selector);
  const $$ = selector => Array.from(document.querySelectorAll(selector));

  const dom = {
    forge: $('#forge'),
    connection: $('#connection'),
    lastUpdate: $('#last-update'),
    topProfile: $('#top-profile'),
    operationLine: $('#operation-line'),
    toast: $('#toast'),
    tooltip: $('#chart-tooltip'),
    modal: $('#confirm-modal'),
    modalTitle: $('#modal-title'),
    modalBody: $('#modal-body'),
    modalConfirm: $('#modal-confirm'),
    modalCancel: $('#modal-cancel'),
    safety: $('#safety-state')
  };

  function setText(selector, value) {
    const element = typeof selector === 'string' ? $(selector) : selector;
    if (!element) return;
    const next = value == null || value === '' ? '—' : String(value);
    if (element.textContent !== next) element.textContent = next;
  }

  function finite(value) {
    return typeof value === 'number' && Number.isFinite(value);
  }

  function average(values) {
    const valid = values.filter(finite);
    return valid.length ? valid.reduce((sum, value) => sum + value, 0) / valid.length : null;
  }

  function valueOrNull(value) {
    return finite(value) ? value : null;
  }

  function formatNumber(value, digits = 0) {
    if (!finite(value)) return '—';
    return new Intl.NumberFormat('en-US', {
      maximumFractionDigits: digits,
      minimumFractionDigits: digits
    }).format(value);
  }

  function formatMHz(value) {
    return finite(value) ? `${formatNumber(value)} MHz` : '—';
  }

  function formatCelsius(value) {
    return finite(value) ? `${formatNumber(value, value % 1 ? 1 : 0)} °C` : '—';
  }

  function formatCurrent(value) {
    return finite(value) ? `${formatNumber(value)} mA` : '—';
  }

  function profileLabel(profile) {
    return PROFILE_LABELS[profile] || String(profile || '—').toUpperCase();
  }

  function addLog(level, message) {
    Store.logs.unshift({
      time: new Date(),
      level: String(level || 'info').toUpperCase(),
      message
    });
    if (Store.logs.length > MAX_LOGS) Store.logs.length = MAX_LOGS;
    const status = level === 'success' ? 'OK' : String(level || 'info').toUpperCase();
    setText(dom.operationLine, `${status} · ${message}`);
    renderSystemLog();
  }

  function showToast(message, error = false) {
    dom.toast.textContent = message;
    dom.toast.className = `show${error ? ' error' : ''}`;
    clearTimeout(showToast.timer);
    showToast.timer = setTimeout(() => {
      dom.toast.className = '';
    }, 3200);
  }

  function setConnection(state, label) {
    dom.connection.className = `status-chip ${state}`;
    dom.connection.textContent = label;
  }

  function apiUrl(path) {
    const query = new URLSearchParams();
    if (TOKEN) query.set('token', TOKEN);
    query.set('_', String(Date.now()));
    return `/api/${path}?${query.toString()}`;
  }

  async function api(path, body = null, method = body == null ? 'GET' : 'POST') {
    const response = await fetch(apiUrl(path), {
      method,
      cache: 'no-store',
      headers: body == null ? {} : {'Content-Type': 'application/json'},
      body: body == null ? undefined : JSON.stringify(body)
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new Error(data.error || `HTTP ${response.status}`);
    }
    return data;
  }

  function connectStream() {
    if (Store.source || Store.reconnectTimer) return;
    setConnection('connecting', 'CONNECTING');
    const source = new EventSource(STREAM_URL);
    Store.source = source;
    source.onopen = () => {
      if (Store.source !== source) return;
      setConnection('live', 'LIVE');
      addLog('success', 'SSE control stream online');
    };
    source.onerror = () => {
      if (Store.source !== source) return;
      source.close();
      Store.source = null;
      Store.reconnectCount += 1;
      setConnection('disconnected', 'OFFLINE');
      addLog('error', 'SSE link lost; reconnect scheduled');
      scheduleReconnect();
    };
    source.onmessage = event => {
      if (Store.source !== source) return;
      Store.reconnectDelay = 1000;
      Store.lastPacketAt = Date.now();
      Store.packets += 1;
      Store.bytes += event.data.length;
      try {
        ingestSnapshot(JSON.parse(event.data));
      } catch (error) {
        addLog('error', `Snapshot parse failed: ${error.message}`);
      }
    };
  }

  function scheduleReconnect() {
    if (Store.reconnectTimer) return;
    const delay = Store.reconnectDelay;
    Store.reconnectDelay = Math.min(delay * 2, 30000);
    Store.reconnectTimer = setTimeout(() => {
      Store.reconnectTimer = null;
      connectStream();
    }, delay);
  }

  function ingestSnapshot(data) {
    Store.snapshot = data;
    const timestamp = Date.parse(data.generated_at);
    const t = Number.isFinite(timestamp) ? timestamp : Date.now();
    if (!Store.firstDataAt) Store.firstDataAt = t;

    if (t > Store.lastSampleAt) {
      Store.lastSampleAt = t;
      Store.samples.push(normalizeSnapshot(data, t));
    }

    const controls = Array.isArray(data.controls) ? data.controls : [];
    const signature = controls.map(control => `${control.name}:${control.min}:${control.max}`).join('|');
    if (signature !== Store.controlsSignature) {
      Store.controlsSignature = signature;
      Store.controls = controls;
      renderAllControlMounts();
    } else {
      Store.controls = controls;
      refreshControlValues();
    }

    updateTelemetryUI(data);
    scheduleAllCharts();
  }

  function normalizeSnapshot(data, t) {
    const cpus = Array.isArray(data.cpu) ? data.cpu : [];
    const mhz = index => valueOrNull(cpus[index]?.current_mhz);
    const thermal = data.thermal_summary || {};
    return {
      t,
      cpuLittle: average([mhz(0), mhz(1), mhz(2), mhz(3)]),
      cpuMid: average([mhz(4), mhz(5), mhz(6)]),
      cpuPrime: mhz(7),
      gpu: valueOrNull(data.gpu?.current_mhz),
      cpuTempLittle: valueOrNull(thermal.cpu_little),
      cpuTempMid: valueOrNull(thermal.cpu_mid),
      cpuTempPrime: valueOrNull(thermal.cpu_prime),
      gpuTemp: valueOrNull(thermal.gpu),
      batteryTemp: valueOrNull(thermal.battery),
      boardTemp: valueOrNull(thermal.board),
      memoryTemp: valueOrNull(thermal.memory),
      batteryCurrent: valueOrNull(data.battery?.current_ma),
      maxTemp: valueOrNull(data.max_temp_c)
    };
  }

  function updateTelemetryUI(data) {
    const cpus = Array.isArray(data.cpu) ? data.cpu : [];
    const cpu = index => cpus[index]?.current_mhz;
    const little = average([cpu(0), cpu(1), cpu(2), cpu(3)]);
    const mid = average([cpu(4), cpu(5), cpu(6)]);
    const prime = valueOrNull(cpu(7));
    const temps = data.thermal_summary || {};
    const socTemps = [temps.cpu_little, temps.cpu_mid, temps.cpu_prime, temps.gpu].filter(finite);
    const socTemp = socTemps.length ? Math.max(...socTemps) : null;
    const profile = data.active_profile || 'manual';
    const generated = new Date(data.generated_at);
    const timeLabel = Number.isNaN(generated.getTime()) ? '—' : generated.toLocaleTimeString();

    setText('#metric-cpu', formatNumber(prime));
    setText('#metric-cpu-detail', `${formatNumber(little)} / ${formatNumber(mid)} / ${formatNumber(prime)} MHz`);
    setText('#metric-gpu', formatNumber(data.gpu?.current_mhz));
    setText('#metric-gpu-detail', data.gpu ? `${data.gpu.current_mhz} / ${data.gpu.max_mhz} MHz` : (data.gpu_error || 'UNAVAILABLE'));
    setText('#metric-soc-temp', formatNumber(socTemp, socTemp % 1 ? 1 : 0));
    setText('#metric-temp-detail', data.watchdog_triggered ? 'WATCHDOG ACTIVE' : 'THERMAL NOMINAL');
    setText('#metric-battery-temp', formatNumber(temps.battery, temps.battery % 1 ? 1 : 0));
    setText('#metric-battery-temp-detail', 'BATTERY THERMAL ZONE');
    setText('#metric-current', formatNumber(data.battery?.current_ma));
    setText('#metric-power-detail', finite(data.battery?.current_ma) ? 'VOLTAGE NOT EXPOSED' : 'NO BATTERY DATA');
    setText('#last-update', `LAST ${timeLabel}`);
    setText('#top-profile', `PROFILE ${profileLabel(profile)}`);
    setText('#ov-gpu-meta', data.gpu ? `${data.gpu.current_mhz} / ${data.gpu.max_mhz} MHz · ${data.gpu.governor || '—'}` : 'GPU DATA UNAVAILABLE');
    setText('#ov-current-meta', formatCurrent(data.battery?.current_ma));

    setText('#cpu-little-current', formatMHz(little));
    setText('#cpu-mid-current', formatMHz(mid));
    setText('#cpu-prime-current', formatMHz(prime));
    setText('#cpu-little-meta', `CPU0-3 · MAX ${formatMHz(cpus[0]?.max_mhz)}`);
    setText('#cpu-mid-meta', `CPU4-6 · MAX ${formatMHz(cpus[4]?.max_mhz)}`);
    setText('#cpu-prime-meta', `CPU7 · MAX ${formatMHz(cpus[7]?.max_mhz)}`);
    setText('#cpu-governor', cpus[0]?.governor || '—');
    setText('#cpu-enforcement', data.enforce_profile ? 'ENFORCED' : 'MONITOR ONLY');

    const cooling = data.controls?.find(control => control.name === 'gpu_thermal_cooling');
    setText('#gpu-lab-meta', data.gpu ? `${data.gpu.current_mhz} / ${data.gpu.max_mhz} MHz · ${data.gpu.governor || '—'}` : 'GPU DATA UNAVAILABLE');
    setText('#gpu-current-metric', data.gpu ? `${formatMHz(data.gpu.current_mhz)} / ${formatMHz(data.gpu.max_mhz)}` : 'UNAVAILABLE');
    setText('#gpu-cooling-metric', cooling ? `${cooling.value} / ${cooling.max}` : 'UNAVAILABLE');

    setText('#thermal-watchdog', data.watchdog_triggered ? 'THERMAL LIMIT ACTIVE' : 'ARMED / NOMINAL');
    setText('#thermal-watchdog-detail', data.config_error || 'CPU 85°C · GPU 85°C · HARD 95°C');
    setText('#thermal-peak', formatCelsius(data.max_temp_c));
    setText('#power-lab-meta', formatCurrent(data.battery?.current_ma));
    setText('#power-current', formatCurrent(data.battery?.current_ma));

    setText('#side-profile', profileLabel(profile));
    setText('#side-enforcement', data.enforce_profile ? 'ENABLED' : 'DISABLED');
    setText('#side-watchdog', data.watchdog_triggered ? 'LIMITING' : 'ARMED');
    setText('#side-cpu-little', formatMHz(little));
    setText('#side-cpu-mid', formatMHz(mid));
    setText('#side-cpu-prime', formatMHz(prime));
    setText('#side-gpu', formatMHz(data.gpu?.current_mhz));
    setText('#side-gpu-max', formatMHz(data.gpu?.max_mhz));
    setText('#side-cooling', cooling ? `${cooling.value} / ${cooling.max}` : '—');
    setText('#side-temp', formatCelsius(socTemp));
    setText('#side-current', formatCurrent(data.battery?.current_ma));
    setText('#side-battery-temp', formatCelsius(temps.battery));
    setText('#diag-packet', generated.toLocaleString());
    setText('#diag-reconnects', Store.reconnectCount);
    setText('#diag-profile', profileLabel(profile));
    setText('#diag-startup', profileLabel(data.startup_profile));
    setText('#diag-enforcement', data.enforce_profile ? 'ENABLED' : 'DISABLED');
    setText('#diag-controls', `${data.controls?.length || 0} WRITABLE`);
    setText('#diag-watchdog', data.watchdog_triggered ? 'ACTIVE' : 'ARMED');

    const alert = $('#side-alert');
    if (data.config_error) {
      alert.className = 'side-alert alert';
      alert.textContent = `CONFIG ERROR · ${data.config_error}`;
    } else if (data.watchdog_triggered) {
      alert.className = 'side-alert alert';
      alert.textContent = `THERMAL WATCHDOG ACTIVE · ${data.last_event?.detail || 'Frequency ceiling reduced'}`;
    } else {
      alert.className = 'side-alert';
      alert.textContent = 'All managed write paths verified. Thermal protection remains authoritative.';
    }
    dom.safety.textContent = data.watchdog_triggered ? 'THERMAL WATCHDOG ACTIVE' : 'THERMAL WATCHDOG ARMED';
    dom.safety.style.color = data.watchdog_triggered ? 'var(--danger)' : 'var(--safe)';

    if (!Store.settingsInitialized && data.profiles?.includes(data.startup_profile)) {
      $('#startup-profile').value = data.startup_profile;
      $('#enforce-profile').value = data.enforce_profile ? 'true' : 'false';
      Store.settingsInitialized = true;
    }
    $$('.profile-card').forEach(card => {
      card.classList.toggle('active', card.dataset.profileCard === profile);
    });
  }

  function refreshDataAge() {
    const age = Store.lastPacketAt ? Math.max(0, Date.now() - Store.lastPacketAt) : null;
    const label = age == null ? '—' : age < 1000 ? `${age} ms` : `${(age / 1000).toFixed(1)} s`;
    setText('#side-age', label);
    setText('#diag-age', label);
    if (age != null && age > 6000 && Store.source) {
      setConnection('disconnected', 'STALE');
    }
  }

  function renderSystemLog() {
    if (Store.page !== 'system') return;
    const container = $('#system-log');
    const fragment = document.createDocumentFragment();
    if (!Store.logs.length) {
      const empty = document.createElement('div');
      empty.className = 'log-entry';
      empty.innerHTML = '<span class="log-time">—</span><span class="log-level">INFO</span><span>No operation events.</span>';
      fragment.appendChild(empty);
    } else {
      Store.logs.forEach(entry => {
        const row = document.createElement('div');
        row.className = `log-entry ${entry.level.toLowerCase()}`;
        const time = document.createElement('span');
        time.className = 'log-time';
        time.textContent = entry.time.toLocaleTimeString();
        const level = document.createElement('span');
        level.className = 'log-level';
        level.textContent = entry.level;
        const message = document.createElement('span');
        message.textContent = entry.message;
        row.append(time, level, message);
        fragment.appendChild(row);
      });
    }
    container.replaceChildren(fragment);
  }

  function controlMatchesScope(control, scope) {
    if (scope === 'cpu') return control.group.startsWith('CPU');
    if (scope === 'gpu') return control.group.startsWith('GPU');
    if (scope === 'thermal') return control.name === 'gpu_thermal_cooling';
    if (scope === 'system') return control.group === '内核调度';
    return false;
  }

  function controlsForScope(scope) {
    return Store.controls.filter(control => controlMatchesScope(control, scope));
  }

  function renderAllControlMounts() {
    $$('.control-mount[data-control-scope]').forEach(mount => {
      renderControlMount(mount, mount.dataset.controlScope);
    });
  }

  function renderControlMount(mount, scope) {
    const controls = controlsForScope(scope);
    const signature = controls.map(control => control.name).join('|');
    if (Store.controlMountSignatures.get(mount) === signature) {
      refreshControlValues();
      return;
    }
    Store.controlMountSignatures.set(mount, signature);
    mount.replaceChildren();
    if (!controls.length) {
      const empty = document.createElement('div');
      empty.className = 'empty-panel';
      empty.innerHTML = '<div><strong>NO CAPABILITY EXPOSED</strong><p>后端没有返回此模块可写的硬件节点。</p></div>';
      mount.appendChild(empty);
      return;
    }

    const batch = document.createElement('div');
    batch.className = 'batch-bar';
    batch.innerHTML = '<span class="batch-count" data-batch-count>0 PENDING WRITES</span><span><button data-batch-reset>DISCARD</button> <button class="primary" data-batch-apply>APPLY CHANGES</button></span>';
    batch.querySelector('[data-batch-reset]').addEventListener('click', () => discardPending(scope));
    batch.querySelector('[data-batch-apply]').addEventListener('click', () => applyPending(scope));
    mount.appendChild(batch);

    const groups = new Map();
    controls.forEach(control => {
      if (!groups.has(control.group)) groups.set(control.group, []);
      groups.get(control.group).push(control);
    });
    groups.forEach((items, groupName) => {
      const group = document.createElement('section');
      group.className = 'control-group';
      const head = document.createElement('div');
      head.className = 'control-group-head';
      head.innerHTML = `<span>${escapeHTML(groupName)}</span><span>${items.length} PARAMETERS</span>`;
      group.appendChild(head);
      items.forEach(control => group.appendChild(buildParameterRow(control)));
      mount.appendChild(group);
    });
    updatePendingCounters();
  }

  function buildParameterRow(control) {
    const row = document.createElement('div');
    row.className = `parameter-row${control.danger ? ' danger' : ''}`;
    row.dataset.controlRow = control.name;

    const top = document.createElement('div');
    top.className = 'parameter-top';
    const name = document.createElement('span');
    name.className = 'parameter-name';
    name.textContent = control.label;
    const risk = document.createElement('span');
    risk.className = 'risk-tag';
    risk.textContent = control.danger ? 'HIGH RISK' : 'WRITABLE';
    top.append(name, risk);
    const path = document.createElement('div');
    path.className = 'parameter-path';
    path.textContent = control.path;
    path.title = control.path;

    const compare = document.createElement('div');
    compare.className = 'value-compare';
    compare.innerHTML = `
      <div class="compare-cell"><span class="compare-label">CURRENT</span><span class="compare-value" data-current-value>${escapeHTML(formatControlValue(control, control.value))}</span></div>
      <div class="compare-cell"><span class="compare-label">TARGET</span><span class="compare-value" data-target-value>${escapeHTML(formatControlValue(control, control.value))}</span></div>
      <div class="compare-cell"><span class="compare-label">VERIFIED</span><span class="compare-value" data-verified-value>${escapeHTML(formatControlValue(control, control.value))}</span></div>`;

    const inputs = document.createElement('div');
    inputs.className = 'parameter-inputs';
    const range = document.createElement('input');
    range.type = 'range';
    range.dataset.rangeInput = control.name;
    range.min = String(control.min);
    range.max = String(control.max);
    range.step = String(Math.max(1, control.step || 1));
    range.value = String(control.value);
    const number = document.createElement('input');
    number.type = 'number';
    number.dataset.numberInput = control.name;
    number.min = String(control.min);
    number.max = String(control.max);
    number.step = String(Math.max(1, control.step || 1));
    number.value = String(control.value);
    number.title = formatControlValue(control, control.value);
    inputs.append(range, number);

    range.addEventListener('input', () => {
      let value = Number(range.value);
      if (control.available?.length && value !== control.max) value = nearestAvailable(control.available, value);
      number.value = String(value);
      stageControl(control, value);
    });
    number.addEventListener('input', () => {
      let value = Number(number.value);
      if (!Number.isFinite(value)) return;
      if (control.available?.length && value !== control.max) value = nearestAvailable(control.available, value);
      range.value = String(value);
      stageControl(control, value);
    });

    const actions = document.createElement('div');
    actions.className = 'parameter-actions';
    const reset = document.createElement('button');
    reset.textContent = 'RESET';
    reset.addEventListener('click', () => resetPendingControl(control));
    const apply = document.createElement('button');
    apply.className = control.danger ? 'warning' : 'primary';
    apply.textContent = 'APPLY';
    apply.addEventListener('click', async () => {
      if (!Store.pending.has(control.name)) stageControl(control, Number(number.value));
      await applyPending(null, [control.name]);
    });
    actions.append(reset, apply);
    row.append(top, path, compare, inputs, actions);
    return row;
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, character => ({
      '&': '&amp;',
      '<': '&lt;',
      '>': '&gt;',
      '"': '&quot;',
      "'": '&#39;'
    })[character]);
  }

  function nearestAvailable(values, target) {
    return values.reduce((best, value) => Math.abs(value - target) < Math.abs(best - target) ? value : best, values[0]);
  }

  function formatControlValue(control, value) {
    if (control.available?.length === 2 && control.min === 0 && control.max === 1) {
      return value ? 'ENABLED' : 'DISABLED';
    }
    if (control.unit === 'kHz') return `${formatNumber(value / 1000, value % 1000 ? 1 : 0)} MHz`;
    if (control.unit === 'Hz') return `${formatNumber(value / 1000000)} MHz`;
    if (control.unit) return `${formatNumber(value, value % 1 ? 1 : 0)} ${control.unit}`;
    return formatNumber(value);
  }

  function stageControl(control, value) {
    Store.pending.set(control.name, Number(value));
    const row = document.querySelector(`[data-control-row="${CSS.escape(control.name)}"]`);
    if (row) {
      row.classList.add('pending');
      setText(row.querySelector('[data-target-value]'), formatControlValue(control, value));
    }
    updatePendingCounters();
  }

  function resetPendingControl(control) {
    Store.pending.delete(control.name);
    const row = document.querySelector(`[data-control-row="${CSS.escape(control.name)}"]`);
    if (row) {
      row.classList.remove('pending');
      setText(row.querySelector('[data-target-value]'), formatControlValue(control, control.value));
      const number = row.querySelector('[data-number-input]');
      const range = row.querySelector('[data-range-input]');
      if (number) number.value = String(control.value);
      if (range) range.value = String(control.value);
    }
    updatePendingCounters();
  }

  function discardPending(scope = null) {
    const names = scope ? controlsForScope(scope).map(control => control.name) : Array.from(Store.pending.keys());
    names.forEach(name => {
      const control = Store.controls.find(item => item.name === name);
      if (control) resetPendingControl(control);
      else Store.pending.delete(name);
    });
    updatePendingCounters();
    addLog('info', `Discarded pending changes${scope ? ` for ${scope.toUpperCase()}` : ''}`);
  }

  function updatePendingCounters() {
    $$('[data-batch-count]').forEach(counter => {
      const mount = counter.closest('.control-mount');
      const scope = mount?.dataset.controlScope;
      const allowed = new Set(controlsForScope(scope).map(control => control.name));
      const count = Array.from(Store.pending.keys()).filter(name => allowed.has(name)).length;
      counter.textContent = `${count} PENDING WRITE${count === 1 ? '' : 'S'}`;
      counter.style.color = count ? 'var(--warning)' : 'var(--muted)';
    });
  }

  function refreshControlValues() {
    Store.controls.forEach(control => {
      $$(`[data-control-row="${CSS.escape(control.name)}"]`).forEach(row => {
        setText(row.querySelector('[data-current-value]'), formatControlValue(control, control.value));
        setText(row.querySelector('[data-verified-value]'), formatControlValue(control, control.value));
        if (!Store.pending.has(control.name)) {
          setText(row.querySelector('[data-target-value]'), formatControlValue(control, control.value));
          const number = row.querySelector('[data-number-input]');
          const range = row.querySelector('[data-range-input]');
          if (number && document.activeElement !== number) number.value = String(control.value);
          if (range && document.activeElement !== range) range.value = String(control.value);
        }
      });
    });
    updatePendingCounters();
  }

  async function applyPending(scope = null, explicitNames = null) {
    const allowed = new Set((scope ? controlsForScope(scope) : Store.controls).map(control => control.name));
    const names = explicitNames || Array.from(Store.pending.keys()).filter(name => allowed.has(name));
    if (!names.length) {
      showToast('NO PENDING CHANGES');
      return;
    }
    const controls = names.map(name => Store.controls.find(control => control.name === name)).filter(Boolean);
    const dangerous = controls.filter(control => control.danger);
    if (dangerous.length) {
      const details = dangerous.map(control => `${control.label}: ${formatControlValue(control, control.value)} → ${formatControlValue(control, Store.pending.get(control.name))}`).join('\n');
      const confirmed = await confirmHardwareWrite('HIGH-RISK HARDWARE WRITE', details);
      if (!confirmed) return;
    }

    let applied = 0;
    for (const control of controls) {
      const target = Store.pending.get(control.name);
      if (!Number.isFinite(target)) continue;
      try {
        const result = await api('set', {param: control.name, value: target});
        const actual = Number(result.value);
        setText(document.querySelector(`[data-control-row="${CSS.escape(control.name)}"] [data-verified-value]`), formatControlValue(control, actual));
        const index = Store.controls.findIndex(item => item.name === control.name);
        if (index >= 0) Store.controls[index] = {...control, value: actual};
        Store.pending.delete(control.name);
        document.querySelector(`[data-control-row="${CSS.escape(control.name)}"]`)?.classList.remove('pending');
        applied += 1;
        addLog('success', `${control.label} write verified at ${formatControlValue(control, actual)}`);
      } catch (error) {
        addLog('error', `${control.label} write failed: ${error.message}`);
        showToast(`WRITE FAILED · ${error.message}`, true);
        break;
      }
    }
    updatePendingCounters();
    if (applied) {
      showToast(`${applied} HARDWARE WRITE${applied === 1 ? '' : 'S'} VERIFIED`);
      scheduleAllCharts();
    }
  }

  function confirmHardwareWrite(title, detail) {
    dom.modalTitle.textContent = title;
    dom.modalBody.textContent = detail;
    dom.modal.classList.add('visible');
    return new Promise(resolve => {
      const finish = answer => {
        dom.modal.classList.remove('visible');
        dom.modalConfirm.onclick = null;
        dom.modalCancel.onclick = null;
        resolve(answer);
      };
      dom.modalConfirm.onclick = () => finish(true);
      dom.modalCancel.onclick = () => finish(false);
    });
  }

  async function applyProfile(profile) {
    if (profile === 'benchmark' || profile === 'system') {
      const message = profile === 'benchmark'
        ? 'Benchmark profile raises minimum frequencies and releases the GPU cooling cap.\nThermal watchdog remains active.'
        : 'System restore writes all saved original values back to managed nodes.';
      if (!await confirmHardwareWrite(`${profileLabel(profile)} / CONFIRM`, message)) return;
    }
    $$(`[data-profile="${profile}"]`).forEach(button => button.disabled = true);
    try {
      const result = await api('profile', {name: profile});
      const failed = result.failed?.length || 0;
      addLog(failed ? 'warn' : 'success', `${profileLabel(profile)} applied; ${result.changed?.length || 0} changed${failed ? `, ${failed} retrying` : ''}`);
      showToast(`${profileLabel(profile)} · ${result.changed?.length || 0} CHANGES${failed ? ` · ${failed} RETRYING` : ''}`, failed > 0);
    } catch (error) {
      addLog('error', `${profileLabel(profile)} failed: ${error.message}`);
      showToast(`PROFILE FAILED · ${error.message}`, true);
    } finally {
      $$(`[data-profile="${profile}"]`).forEach(button => button.disabled = false);
    }
  }

  async function saveBootPolicy() {
    const button = $('#save-settings');
    button.disabled = true;
    try {
      const result = await api('config', {
        startup_profile: $('#startup-profile').value,
        enforce_profile: $('#enforce-profile').value === 'true'
      });
      addLog('success', `Boot profile saved: ${profileLabel(result.config?.startup_profile)}; enforcement ${result.config?.enforce_profile ? 'enabled' : 'disabled'}`);
      showToast('BOOT POLICY SAVED');
    } catch (error) {
      addLog('error', `Boot policy save failed: ${error.message}`);
      showToast(`SAVE FAILED · ${error.message}`, true);
    } finally {
      button.disabled = false;
    }
  }

  async function restoreAll() {
    if (!await confirmHardwareWrite('RESTORE ALL MANAGED NODES', 'The daemon will restore every backed-up original value and verify each readback.')) return;
    try {
      const result = await api('restore', null, 'POST');
      addLog('success', `Restore complete: ${result.restored?.length || 0} nodes verified`);
      showToast(`RESTORED ${result.restored?.length || 0} NODES`);
    } catch (error) {
      addLog('error', `Restore failed: ${error.message}`);
      showToast(`RESTORE FAILED · ${error.message}`, true);
    }
  }

  function exportConfiguration() {
    const controls = {};
    Store.controls.forEach(control => {
      controls[control.name] = {
        label: control.label,
        group: control.group,
        value: control.value,
        unit: control.unit || '',
        path: control.path
      };
    });
    const exportData = {
      product: 'VOLT FORGE',
      schema: 'odin-tuner-config/v1',
      exported_at: new Date().toISOString(),
      active_profile: Store.snapshot?.active_profile || null,
      startup_profile: $('#startup-profile').value,
      enforce_profile: $('#enforce-profile').value === 'true',
      controls
    };
    const blob = new Blob([JSON.stringify(exportData, null, 2)], {type: 'application/json'});
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = `volt-forge-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
    anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    addLog('success', 'Configuration exported to local JSON');
  }

  async function importConfiguration(file) {
    try {
      const text = await file.text();
      const data = JSON.parse(text);
      if (data.schema !== 'odin-tuner-config/v1' || !data.controls) {
        throw new Error('unsupported configuration schema');
      }
      let staged = 0;
      Object.entries(data.controls).forEach(([name, entry]) => {
        const control = Store.controls.find(item => item.name === name);
        const value = Number(entry.value);
        if (control && Number.isFinite(value) && value <= control.max && value >= control.min) {
          stageControl(control, value);
          staged += 1;
        }
      });
      if (data.startup_profile) $('#startup-profile').value = data.startup_profile;
      if (typeof data.enforce_profile === 'boolean') $('#enforce-profile').value = String(data.enforce_profile);
      addLog('success', `Imported ${staged} values into pending queue; no hardware write performed`);
      showToast(`${staged} VALUES STAGED · REVIEW BEFORE APPLY`);
    } catch (error) {
      addLog('error', `Configuration import failed: ${error.message}`);
      showToast(`IMPORT FAILED · ${error.message}`, true);
    } finally {
      $('#import-file').value = '';
    }
  }

  class TimeChart {
    constructor(canvas, config) {
      this.canvas = canvas;
      this.ctx = canvas.getContext('2d');
      this.config = config;
      this.hoverX = null;
      this.scheduled = false;
      this.lastDraw = 0;
      this.mouseMove = this.mouseMove.bind(this);
      this.mouseLeave = this.mouseLeave.bind(this);
      this.schedule = this.schedule.bind(this);
      canvas.addEventListener('mousemove', this.mouseMove);
      canvas.addEventListener('mouseleave', this.mouseLeave);
      new ResizeObserver(this.schedule).observe(canvas);
    }

    sampleWindow() {
      const samples = Store.samples.values();
      if (!samples.length) return [];
      const end = samples[samples.length - 1].t;
      const start = end - Store.range;
      return samples.filter(sample => sample.t >= start && sample.t <= end);
    }

    schedule() {
      if (this.scheduled) return;
      this.scheduled = true;
      const frame = timestamp => {
        if (timestamp - this.lastDraw < MIN_RENDER_INTERVAL) {
          requestAnimationFrame(frame);
          return;
        }
        this.scheduled = false;
        this.lastDraw = timestamp;
        this.draw();
      };
      requestAnimationFrame(frame);
    }

    resize() {
      const dpr = window.devicePixelRatio || 1;
      const width = this.canvas.clientWidth;
      const height = this.canvas.clientHeight;
      if (!width || !height) return null;
      const pixelWidth = Math.round(width * dpr);
      const pixelHeight = Math.round(height * dpr);
      if (this.canvas.width !== pixelWidth || this.canvas.height !== pixelHeight) {
        this.canvas.width = pixelWidth;
        this.canvas.height = pixelHeight;
      }
      this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      return {width, height};
    }

    draw() {
      const page = this.canvas.closest('.page');
      if (document.visibilityState === 'hidden' || (page && !page.classList.contains('active'))) return;
      const size = this.resize();
      if (!size) return;
      const {ctx, config} = this;
      const {width, height} = size;
      const margin = {left: 54, right: 15, top: 12, bottom: 24};
      const plotW = width - margin.left - margin.right;
      const plotH = height - margin.top - margin.bottom;
      const samples = this.sampleWindow();
      const empty = this.canvas.parentElement.querySelector('.chart-empty');
      empty?.classList.toggle('hidden', samples.length > 0);
      ctx.clearRect(0, 0, width, height);
      ctx.fillStyle = '#0d1218';
      ctx.fillRect(0, 0, width, height);
      if (!samples.length) return;

      const end = samples[samples.length - 1].t;
      const start = end - Store.range;
      const values = [];
      samples.forEach(sample => config.series.forEach(series => {
        const value = series.get(sample);
        if (finite(value)) values.push(value);
      }));
      let minY = config.minY;
      let maxY = config.maxY;
      if (!finite(minY) || !finite(maxY)) {
        minY = values.length ? Math.min(...values) : 0;
        maxY = values.length ? Math.max(...values) : 1;
        const pad = Math.max((maxY - minY) * .14, 1);
        if (config.includeZero) minY = Math.min(0, minY - pad);
        else minY -= pad;
        maxY += pad;
      }
      if (maxY <= minY) maxY = minY + 1;

      const xOf = t => margin.left + (t - start) / Math.max(1, end - start) * plotW;
      const yOf = v => margin.top + (maxY - v) / (maxY - minY) * plotH;

      ctx.font = '9px "SFMono-Regular", "Roboto Mono", monospace';
      ctx.textBaseline = 'middle';
      ctx.lineWidth = 1;
      for (let row = 0; row <= 4; row++) {
        const y = margin.top + plotH * row / 4;
        ctx.strokeStyle = '#1d2936';
        ctx.beginPath();
        ctx.moveTo(margin.left, y);
        ctx.lineTo(width - margin.right, y);
        ctx.stroke();
        const label = maxY - (maxY - minY) * row / 4;
        ctx.fillStyle = '#667585';
        ctx.fillText(`${formatAxis(label, config.axisDigits)}${config.unit || ''}`, 3, y);
      }
      for (let col = 0; col <= 4; col++) {
        const x = margin.left + plotW * col / 4;
        ctx.strokeStyle = '#17212c';
        ctx.beginPath();
        ctx.moveTo(x, margin.top);
        ctx.lineTo(x, margin.top + plotH);
        ctx.stroke();
        const t = start + (end - start) * col / 4;
        ctx.fillStyle = '#667585';
        ctx.textAlign = col === 0 ? 'left' : col === 4 ? 'right' : 'center';
        ctx.fillText(new Date(t).toLocaleTimeString([], {hour12: false}), x, height - 8);
      }
      ctx.textAlign = 'left';

      config.series.forEach(series => {
        const segments = [];
        let current = [];
        samples.forEach(sample => {
          const value = series.get(sample);
          if (finite(value)) {
            current.push({x: sample.t, y: value});
          } else if (current.length) {
            segments.push(current);
            current = [];
          }
        });
        if (current.length) segments.push(current);
        segments.forEach(points => {
          if (points.length) this.drawSmoothSeries(points, xOf, yOf, series.color);
        });
        const last = segments.at(-1)?.at(-1);
        if (!last) return;
        ctx.fillStyle = series.color;
        ctx.beginPath();
        ctx.arc(xOf(last.x), yOf(last.y), 2.4, 0, Math.PI * 2);
        ctx.fill();
      });

      if (this.hoverX != null && this.hoverX >= margin.left && this.hoverX <= width - margin.right) {
        this.drawCrosshair(samples, start, end, margin, plotW, plotH, width, minY, maxY, xOf, yOf, config.series);
      }
    }

    drawSmoothSeries(points, xOf, yOf, color) {
      const ctx = this.ctx;
      if (points.length === 1) {
        ctx.fillStyle = color;
        ctx.beginPath();
        ctx.arc(xOf(points[0].x), yOf(points[0].y), 2, 0, Math.PI * 2);
        ctx.fill();
        return;
      }
      const slopes = monotoneSlopes(points);
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.8;
      ctx.lineJoin = 'round';
      ctx.lineCap = 'round';
      ctx.beginPath();
      ctx.moveTo(xOf(points[0].x), yOf(points[0].y));
      for (let index = 0; index < points.length - 1; index++) {
        const p0 = points[index];
        const p1 = points[index + 1];
        const dx = p1.x - p0.x;
        const cp1x = xOf(p0.x + dx / 3);
        const cp1y = yOf(p0.y + slopes[index] * dx / 3);
        const cp2x = xOf(p1.x - dx / 3);
        const cp2y = yOf(p1.y - slopes[index + 1] * dx / 3);
        ctx.bezierCurveTo(cp1x, cp1y, cp2x, cp2y, xOf(p1.x), yOf(p1.y));
      }
      ctx.stroke();
    }

    drawCrosshair(samples, start, end, margin, plotW, plotH, width, minY, maxY, xOf, yOf, series) {
      const t = start + (this.hoverX - margin.left) / plotW * (end - start);
      const nearest = samples.reduce((best, sample) => Math.abs(sample.t - t) < Math.abs(best.t - t) ? sample : best, samples[0]);
      const x = xOf(nearest.t);
      const ctx = this.ctx;
      ctx.strokeStyle = '#748293';
      ctx.setLineDash([3, 3]);
      ctx.beginPath();
      ctx.moveTo(x, margin.top);
      ctx.lineTo(x, margin.top + plotH);
      ctx.stroke();
      ctx.setLineDash([]);
      series.forEach(item => {
        const value = item.get(nearest);
        if (!finite(value)) return;
        ctx.fillStyle = item.color;
        ctx.beginPath();
        ctx.arc(x, yOf(value), 3.2, 0, Math.PI * 2);
        ctx.fill();
      });
      this.showTooltip(nearest, series, this.hoverX, width);
    }

    showTooltip(sample, series, localX, width) {
      const rows = series.map(item => {
        const value = item.get(sample);
        return finite(value)
          ? `<div class="tooltip-row"><span><i style="--series:${item.color}"></i>${escapeHTML(item.label)}</span><strong>${escapeHTML(formatTooltipValue(value, this.config.unit))}</strong></div>`
          : '';
      }).join('');
      dom.tooltip.innerHTML = `<div class="tooltip-time">${new Date(sample.t).toLocaleString()}</div>${rows}`;
      dom.tooltip.classList.add('visible');
      const rect = this.canvas.getBoundingClientRect();
      const left = Math.min(window.innerWidth - 190, rect.left + localX + 14);
      dom.tooltip.style.left = `${Math.max(8, left)}px`;
      dom.tooltip.style.top = `${Math.min(window.innerHeight - 120, rect.top + 24)}px`;
    }

    mouseMove(event) {
      const rect = this.canvas.getBoundingClientRect();
      this.hoverX = event.clientX - rect.left;
      this.schedule();
    }

    mouseLeave() {
      this.hoverX = null;
      dom.tooltip.classList.remove('visible');
      this.schedule();
    }
  }

  function monotoneSlopes(points) {
    const n = points.length;
    const delta = new Array(n - 1);
    for (let i = 0; i < n - 1; i++) {
      delta[i] = (points[i + 1].y - points[i].y) / Math.max(1, points[i + 1].x - points[i].x);
    }
    const slopes = new Array(n).fill(0);
    slopes[0] = delta[0];
    slopes[n - 1] = delta[n - 2];
    for (let i = 1; i < n - 1; i++) {
      if (delta[i - 1] * delta[i] <= 0) {
        slopes[i] = 0;
      } else {
        const dx0 = Math.max(1, points[i].x - points[i - 1].x);
        const dx1 = Math.max(1, points[i + 1].x - points[i].x);
        slopes[i] = (delta[i - 1] + delta[i]) / (dx0 + dx1);
      }
    }
    for (let i = 0; i < n - 1; i++) {
      if (delta[i] === 0) {
        slopes[i] = 0;
        slopes[i + 1] = 0;
        continue;
      }
      const a = slopes[i] / delta[i];
      const b = slopes[i + 1] / delta[i];
      const h = a * a + b * b;
      if (h > 9) {
        const scale = 3 / Math.sqrt(h);
        slopes[i] = scale * a * delta[i];
        slopes[i + 1] = scale * b * delta[i];
      }
    }
    return slopes;
  }

  function formatAxis(value, digits) {
    if (Math.abs(value) >= 1000) return `${(value / 1000).toFixed(1)}k`;
    return Number(value).toFixed(digits || 0);
  }

  function formatTooltipValue(value, unit) {
    if (unit === ' MHz') return `${formatNumber(value)} MHz`;
    if (unit === ' °C') return `${formatNumber(value, 1)} °C`;
    if (unit === ' mA') return `${formatNumber(value)} mA`;
    return `${formatNumber(value, 1)}${unit || ''}`;
  }

  function createCharts() {
    const series = {
      little: {label: 'LITTLE', color: COLORS.cpu, get: sample => sample.cpuLittle},
      mid: {label: 'MID', color: COLORS.mid, get: sample => sample.cpuMid},
      prime: {label: 'PRIME', color: COLORS.prime, get: sample => sample.cpuPrime},
      gpu: {label: 'GPU', color: COLORS.gpu, get: sample => sample.gpu},
      cpuTempLittle: {label: 'CPU LITTLE', color: COLORS.cpu, get: sample => sample.cpuTempLittle},
      cpuTempMid: {label: 'CPU MID', color: COLORS.mid, get: sample => sample.cpuTempMid},
      cpuTempPrime: {label: 'CPU PRIME', color: COLORS.prime, get: sample => sample.cpuTempPrime},
      gpuTemp: {label: 'GPU', color: COLORS.gpu, get: sample => sample.gpuTemp},
      batteryTemp: {label: 'BATTERY', color: COLORS.battery, get: sample => sample.batteryTemp},
      boardTemp: {label: 'BOARD', color: COLORS.board, get: sample => sample.boardTemp},
      current: {label: 'CURRENT', color: COLORS.battery, get: sample => sample.batteryCurrent}
    };
    const add = (selector, config) => {
      const canvas = $(selector);
      if (canvas) Store.charts.push(new TimeChart(canvas, config));
    };
    add('#ov-cpu-chart', {series: [series.little, series.mid, series.prime], unit: ' MHz', minY: 0, maxY: 3200});
    add('#ov-gpu-chart', {series: [series.gpu], unit: ' MHz', minY: 0, maxY: 900});
    add('#ov-temp-chart', {series: [series.cpuTempPrime, series.gpuTemp, series.batteryTemp, series.boardTemp], unit: ' °C', minY: 20, maxY: 100});
    add('#ov-current-chart', {series: [series.current], unit: ' mA', includeZero: true});
    add('#cpu-lab-chart', {series: [series.little, series.mid, series.prime], unit: ' MHz', minY: 0, maxY: 3200});
    add('#gpu-lab-chart', {series: [series.gpu], unit: ' MHz', minY: 0, maxY: 900});
    add('#thermal-lab-chart', {series: [series.cpuTempLittle, series.cpuTempMid, series.cpuTempPrime, series.gpuTemp, series.batteryTemp, series.boardTemp], unit: ' °C', minY: 20, maxY: 100});
    add('#power-lab-chart', {series: [series.current], unit: ' mA', includeZero: true});
  }

  function scheduleAllCharts() {
    if (document.visibilityState === 'hidden') return;
    Store.charts.forEach(chart => chart.schedule());
  }

  function goToPage(page, updateHash = true) {
    const valid = $$('.page[data-page-view]').some(section => section.dataset.pageView === page);
    if (!valid) page = 'overview';
    Store.page = page;
    $$('.page[data-page-view]').forEach(section => section.classList.toggle('active', section.dataset.pageView === page));
    $$('.nav-button[data-page]').forEach(button => button.classList.toggle('active', button.dataset.page === page));
    if (updateHash) history.replaceState(null, '', `#${page}`);
    const mount = document.querySelector(`#page-${page} .control-mount`);
    if (mount) renderControlMount(mount, mount.dataset.controlScope);
    if (page === 'system') renderSystemLog();
    scheduleAllCharts();
  }

  function setupEvents() {
    $('#rail-nav').addEventListener('click', event => {
      const button = event.target.closest('.nav-button[data-page]');
      if (button) goToPage(button.dataset.page);
    });
    $('#footer-system').addEventListener('click', () => goToPage('system'));
    $('#footer-restore').addEventListener('click', restoreAll);
    $('#rail-restore').addEventListener('click', restoreAll);
    $('#restore-all').addEventListener('click', restoreAll);
    $('#save-settings').addEventListener('click', saveBootPolicy);
    $('#export-config').addEventListener('click', exportConfiguration);
    $('#import-config').addEventListener('click', () => $('#import-file').click());
    $('#import-file').addEventListener('change', event => {
      const file = event.target.files?.[0];
      if (file) importConfiguration(file);
    });
    document.addEventListener('click', event => {
      const profileButton = event.target.closest('[data-profile]');
      if (profileButton) applyProfile(profileButton.dataset.profile);
    });
    $$('.range-button').forEach(button => {
      button.addEventListener('click', () => {
        Store.range = Number(button.dataset.range);
        $$('.range-button').forEach(item => item.classList.toggle('active', Number(item.dataset.range) === Store.range));
        scheduleAllCharts();
        addLog('info', `Chart window set to ${RANGE_LABELS.get(Store.range) || Store.range}`);
      });
    });
    $('#side-toggle').addEventListener('click', () => {
      dom.forge.classList.toggle('side-collapsed');
      localStorage.setItem('volt-forge-side-collapsed', dom.forge.classList.contains('side-collapsed') ? '1' : '0');
      setTimeout(scheduleAllCharts, 220);
    });
    $('#clear-log').addEventListener('click', () => {
      Store.logs = [];
      renderSystemLog();
    });
    window.addEventListener('resize', scheduleAllCharts);
    window.addEventListener('hashchange', () => goToPage(location.hash.slice(1), false));
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') {
        const stale = Store.lastPacketAt && Date.now() - Store.lastPacketAt > 6000;
        if (stale && Store.source) {
          Store.source.close();
          Store.source = null;
          Store.reconnectDelay = 1000;
          setConnection('disconnected', 'RECONNECTING');
          scheduleReconnect();
        } else if (!Store.source && !Store.reconnectTimer) {
          Store.reconnectDelay = 1000;
          connectStream();
        }
        scheduleAllCharts();
      }
    });
  }

  function setupStaticDiagnostics() {
    setText('#diag-host', location.host);
    setText('#diag-agent', navigator.userAgent);
    setText('#top-kernel', '5.4.281-QGKI');
    setText('#diag-kernel', '5.4.281-qgki-g6f620856e503');
  }

  function init() {
    if (localStorage.getItem('volt-forge-side-collapsed') === '1') {
      dom.forge.classList.add('side-collapsed');
    }
    setupStaticDiagnostics();
    setupEvents();
    createCharts();
    addLog('info', 'VOLT FORGE frontend initialized');
    goToPage(location.hash.slice(1) || 'overview', false);
    connectStream();
    setInterval(refreshDataAge, 1000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init, {once: true});
  } else {
    init();
  }
})();
