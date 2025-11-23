(() => {
  const testLogEntries = [];
  let consoleBackup = null;
  let consoleHooked = false;

  const pushLog = (msg) => {
    const entry = { ts: new Date().toISOString(), msg };
    testLogEntries.push(entry);
  };

  const log = (msg) => {
    pushLog(msg);
    console.log(`[UI TEST] ${msg}`);
  };
  const assert = (condition, msg) => {
    if (!condition) {
      throw new Error(`UI TEST FAILED: ${msg}`);
    }
  };

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

  const waitFor = async (predicate, timeout = 5000) => {
    const start = Date.now();
    while (Date.now() - start < timeout) {
      if (predicate()) {
        return true;
      }
      await sleep(50);
    }
    throw new Error('UI TEST TIMEOUT');
  };

  const inputs = {
    from: document.getElementById('from'),
    to: document.getElementById('to'),
    step: document.getElementById('step'),
    speed: document.getElementById('speed'),
    window: document.getElementById('window'),
  };

  const controls = {
    playPause: document.getElementById('playPauseBtn'),
    stop: document.getElementById('stopBtn'),
    stepBack: document.getElementById('stepBackBtn'),
    stepFwd: document.getElementById('stepFwdBtn'),
    jumpStart: document.getElementById('jumpStartBtn'),
    jumpEnd: document.getElementById('jumpEndBtn'),
    rangeBtn: document.getElementById('rangeBtn'),
    timeline: document.getElementById('timeline'),
  };

  const setRange = (from, to) => {
    inputs.from.value = from;
    inputs.to.value = to;
  };

  const click = (el) => {
    if (!el) throw new Error('UI TEST FAILED: element is missing');
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  };

  const waitForRangeButton = async () => {
    if (!controls.rangeBtn) throw new Error('UI TEST FAILED: range button missing');
    await waitFor(() => !controls.rangeBtn.disabled, 8000);
  };

  const ensureIdle = async () => {
    log('Waiting for idle state...');
    try {
      await waitFor(() => document.getElementById('statusBadge').textContent === 'idle', 10000);
    } catch (err) {
      console.warn('[UI TEST] Idle wait timed out, continuing anyway');
    }
  };

  const waitStatus = async (target, timeout = 10000) => {
    log(`Waiting for status ${target}...`);
    try {
      await waitFor(() => statusNormalize(document.getElementById('statusBadge').textContent) === target, timeout);
    } catch (err) {
      console.warn(`[UI TEST] Status ${target} wait timed out`);
    }
  };
  const currentStatus = () => statusNormalize(document.getElementById('statusBadge').textContent);
  const clickIfActive = (el, label) => {
    const st = currentStatus();
    if (st === 'running' || st === 'paused' || st === 'stopping') {
      click(el);
      log(`${label} clicked`);
    } else {
      log(`${label} skipped (status ${st})`);
    }
  };

  const waitStatusIn = async (targets, timeout = 10000) => {
    const targetSet = new Set(targets.map(statusNormalize));
    const normalized = (s) => statusNormalize(s);
    log(`Waiting for statuses ${targets.join(', ')}...`);
    try {
      await waitFor(() => targetSet.has(normalized(document.getElementById('statusBadge').textContent)), timeout);
    } catch (err) {
      console.warn(`[UI TEST] Status ${Array.from(targetSet)} wait timed out, current=${document.getElementById('statusBadge').textContent}`);
    }
  };

  const seekFraction = async (fraction) => {
    const value = Math.max(0, Math.min(1, fraction)) * 1000;
    controls.timeline.value = Math.round(value);
    controls.timeline.dispatchEvent(new Event('input', { bubbles: true }));
    await sleep(50);
    controls.timeline.dispatchEvent(new Event('change', { bubbles: true }));
    await sleep(150);
  };

  const ensureStopped = async () => {
    const st = currentStatus();
    if (st === 'running' || st === 'paused' || st === 'stopping') {
      clickIfActive(controls.stop, 'Stop');
      await waitStatusIn(['done', 'idle'], 5000);
    }
  };

  const run = async () => {
    try {
      log('Starting UI smoke test');
      await waitForRangeButton();
      click(controls.rangeBtn);
      await waitFor(() => inputs.from.value !== '' && inputs.to.value !== '', 2000);
      setRange(inputs.from.value, inputs.to.value);
      await sleep(500);
      await seekFraction(0);
      log('Seeking mid range');
      controls.timeline.value = 500;
      controls.timeline.dispatchEvent(new Event('input'));
      await sleep(100);
      controls.timeline.dispatchEvent(new Event('change'));
      await sleep(200);
      await waitFor(() => document.getElementById('currentLabel').textContent !== '-', 2000);
      const manual = document.getElementById('currentLabel').textContent;
      log(`Manual selection: ${manual}`);
      log('Starting playback');
      click(controls.playPause);
      await waitStatus('running');
      await waitFor(() => document.getElementById('statTs').textContent === manual, 6000);
      log(`Playback started from ${document.getElementById('statTs').textContent}`);
      log('Pausing');
      click(controls.playPause);
      await waitStatus('paused');
      log(`Current label after pause: ${document.getElementById('currentLabel').textContent}`);
      log('Step forward/backward');
      clickIfActive(controls.stepFwd, 'Step forward');
      await sleep(200);
      clickIfActive(controls.stepBack, 'Step backward');
      await sleep(200);
      log('Jump to start');
      click(controls.jumpStart);
      await sleep(200);
      log('Jump to end');
      click(controls.jumpEnd);
      await sleep(200);
      log('Stopping');
      clickIfActive(controls.stop, 'Stop');
      await ensureIdle();
      log('UI smoke test PASSED');
    } catch (err) {
      console.error(err);
    }
  };

  const runFlow = async () => {
    try {
      log('Starting UI flow test (range → seek → start → pause → seek → resume → stop)');
      await ensureStopped();
      await waitForRangeButton();
      click(controls.rangeBtn);
      await waitStatusIn(['pending', 'paused', 'idle'], 8000);
      await seekFraction(0);

      // Готовим параметры: чуть замедлить, чтобы успеть поставить паузу.
      inputs.speed.value = '0.5';
      await seekFraction(0.25); // pending seek
      await waitStatusIn(['pending', 'paused', 'running'], 5000);

      // start
      click(controls.playPause);
      await waitStatusIn(['running', 'paused', 'done'], 8000);

      // pause
      click(controls.playPause);
      await waitStatusIn(['paused', 'done'], 5000);

      // seek while paused
      await seekFraction(0.75);
      await waitStatusIn(['paused', 'pending'], 4000);

      // resume
      clickIfActive(controls.playPause, 'Resume');
      await waitStatusIn(['running', 'done'], 8000);

      // stop
      clickIfActive(controls.stop, 'Stop');
      await waitStatusIn(['done', 'idle'], 8000);

      log('UI flow test PASSED');
    } catch (err) {
      console.error(err);
    }
  };

  window.__timemachineUITest = run;
  window.__timemachineUIFlowTest = runFlow;
  log('UI test helper ready: call `window.__timemachineUITest()`');

  let statusNode = null;
  let statusStyleInjected = false;
  let statusTextNode = null;

  const setStatusIndicator = (text, active) => {
    if (!statusNode) return;
    if (statusTextNode) statusTextNode.textContent = text || '';
    if (active && text) {
      statusNode.classList.add('active');
      statusNode.style.opacity = '1';
    } else {
      statusNode.classList.remove('active');
      statusNode.style.opacity = '0';
      if (statusTextNode) statusTextNode.textContent = '';
    }
  };

  const snapshotTimeline = () => {
    const slider = document.getElementById('timeline');
    const current = document.getElementById('currentLabel');
    return {
      value: slider ? slider.value : null,
      label: current ? current.textContent : null,
      previewTs: state.previewTs,
    };
  };

  const restoreTimeline = (snap) => {
    if (!snap) return;
    const slider = document.getElementById('timeline');
    const current = document.getElementById('currentLabel');
    state.previewTs = snap.previewTs || null;
    if (slider && snap.value !== null && snap.value !== undefined) {
      slider.value = snap.value;
      slider.dispatchEvent(new Event('input', { bubbles: true }));
    }
    if (current && snap.label) {
      current.textContent = snap.label;
    }
  };

  const collectUILog = () => {
    const logEl = document.getElementById('log');
    if (!logEl) return [];
    return Array.from(logEl.querySelectorAll('.log-entry')).map((el) => el.textContent.trim()).filter(Boolean);
  };

  const saveLogsToStorage = (errorMessage, status = 'ok') => {
    try {
      const payload = {
        saved_at: new Date().toISOString(),
        status,
        error: errorMessage || null,
        entries: testLogEntries.slice(),
        uiLog: collectUILog(),
      };
      localStorage.setItem('tm-ui-test-log', JSON.stringify(payload));
      log('Лог теста сохранён в localStorage (tm-ui-test-log)');
    } catch (err) {
      console.error('[UI TEST] saveLogsToStorage failed', err);
    }
  };

  const downloadStoredLogs = () => {
    try {
      const raw = localStorage.getItem('tm-ui-test-log');
      if (!raw) {
        log('Нет сохранённых логов для скачивания');
        return;
      }
      const data = JSON.parse(raw);
      const pretty = JSON.stringify(data, null, 2);
      const blob = new Blob([pretty], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'tm-ui-test-log.json';
      document.body.appendChild(a);
      a.click();
      setTimeout(() => {
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      }, 0);
      log('Сохранённый лог скачан');
    } catch (err) {
      pushLog(`DOWNLOAD ERROR: ${err?.message || err}`);
      console.error('[UI TEST] downloadStoredLogs failed', err);
    }
  };

  const syncDownloadVisibility = () => {
    const raw = localStorage.getItem('tm-ui-test-log');
    const btn = document.getElementById('ui-test-download');
    if (!btn) return;
    btn.style.display = raw ? 'inline-block' : 'none';
  };

  // Форсируем появление индикатора даже при быстрых нажатиях.
  const showIndicatorImmediately = (text) => {
    setStatusIndicator(text, true);
    requestAnimationFrame(() => setStatusIndicator(text, true));
    setTimeout(() => setStatusIndicator(text, true), 50);
  };

  const hookConsole = () => {
    if (consoleHooked) return;
    consoleHooked = true;
    consoleBackup = {
      log: console.log,
      info: console.info,
      warn: console.warn,
      error: console.error,
    };
    const wrap = (level) => (...args) => {
      pushLog(`${level.toUpperCase()}: ${args.map((a) => (typeof a === 'string' ? a : JSON.stringify(a))).join(' ')}`);
      if (consoleBackup && consoleBackup[level]) {
        consoleBackup[level](...args);
      }
    };
    console.log = wrap('log');
    console.info = wrap('info');
    console.warn = wrap('warn');
    console.error = wrap('error');
  };

  const restoreConsole = () => {
    if (!consoleHooked || !consoleBackup) return;
    console.log = consoleBackup.log;
    console.info = consoleBackup.info;
    console.warn = consoleBackup.warn;
    console.error = consoleBackup.error;
    consoleHooked = false;
    consoleBackup = null;
  };

  const runLocked = (btn, fn, label) => {
    if (!btn || btn.disabled) return;
    const savedTimeline = snapshotTimeline();
    const original = btn.textContent;
    btn.disabled = true;
    btn.style.opacity = '0.6';
    btn.textContent = label || original;
    showIndicatorImmediately('Идёт тестирование…');
    hookConsole();
    (async () => {
      try {
        await fn();
        saveLogsToStorage(null, 'ok');
        syncDownloadVisibility();
      } catch (err) {
        pushLog(`ERROR: ${err?.message || err}`);
        console.error(err);
        saveLogsToStorage(err?.message || String(err), 'error');
      } finally {
        try {
          await ensureStopped();
        } catch (stopErr) {
          pushLog(`STOP ERROR: ${stopErr?.message || stopErr}`);
          console.error(stopErr);
        }
        restoreTimeline(savedTimeline);
        restoreConsole();
        btn.disabled = false;
        btn.style.opacity = '';
        btn.textContent = original;
        setStatusIndicator('', false);
        syncDownloadVisibility();
      }
    })();
  };

  const createTestBtn = () => {
    if (document.getElementById('ui-test-runner')) return;
    if (!statusStyleInjected) {
      const style = document.createElement('style');
      style.textContent = `
        @keyframes tm-test-pulse {
          0% { box-shadow: 0 0 0 0 rgba(52,211,153,0.35); }
          100% { box-shadow: 0 0 0 10px rgba(52,211,153,0); }
        }
        #ui-test-status.active {
          background: linear-gradient(135deg, rgba(16,185,129,0.9), rgba(5,150,105,0.9));
          color: #0b1220;
          border: 1px solid rgba(16,185,129,0.6);
          animation: tm-test-pulse 1s ease-in-out infinite;
        }
      `;
      document.head.appendChild(style);
      statusStyleInjected = true;
    }
    const wrap = document.createElement('div');
    wrap.id = 'ui-test-runner';
    wrap.style.position = 'fixed';
    wrap.style.top = '12px';
    wrap.style.right = '12px';
    wrap.style.zIndex = '9999';
    wrap.style.display = 'flex';
    wrap.style.flexDirection = 'column';
    wrap.style.gap = '8px';

    statusNode = document.createElement('div');
    statusNode.id = 'ui-test-status';
    statusNode.style.display = 'inline-flex';
    statusNode.style.alignItems = 'center';
    statusNode.style.gap = '8px';
    statusNode.style.padding = '10px 14px';
    statusNode.style.borderRadius = '12px';
    statusNode.style.background = 'rgba(17,24,39,0.9)';
    statusNode.style.color = '#34d399';
    statusNode.style.border = '1px solid rgba(52,211,153,0.5)';
    statusNode.style.fontWeight = '700';
    statusNode.style.boxShadow = '0 12px 30px rgba(0,0,0,0.35)';
    statusNode.style.transition = 'opacity 0.2s ease';
    statusNode.style.opacity = '0';
    statusNode.style.marginLeft = '12px';
    statusNode.style.whiteSpace = 'nowrap';
    const dot = document.createElement('span');
    dot.style.display = 'block';
    dot.style.width = '10px';
    dot.style.height = '10px';
    dot.style.borderRadius = '50%';
    dot.style.background = '#22c55e';
    dot.style.boxShadow = '0 0 0 6px rgba(34,197,94,0.2)';
    statusTextNode = document.createElement('span');
    statusTextNode.id = 'ui-test-status-text';
    statusNode.appendChild(dot);
    statusNode.appendChild(statusTextNode);

    const btn = document.createElement('button');
    btn.textContent = 'Запустить smoke';
    btn.style.padding = '10px 14px';
    btn.style.borderRadius = '12px';
    btn.style.border = 'none';
    btn.style.background = '#0ea5e9';
    btn.style.color = '#fff';
    btn.style.fontWeight = '700';
    btn.style.cursor = 'pointer';
    btn.style.boxShadow = '0 8px 24px rgba(14,165,233,0.3)';
    btn.addEventListener('click', () => {
      log('Запуск UI smoke теста по кнопке');
      runLocked(btn, run, 'Выполняется…');
    });

    const btnFlow = document.createElement('button');
    btnFlow.textContent = 'Запустить flow';
    btnFlow.style.padding = '10px 14px';
    btnFlow.style.borderRadius = '12px';
    btnFlow.style.border = 'none';
    btnFlow.style.background = '#22d3ee';
    btnFlow.style.color = '#0f172a';
    btnFlow.style.fontWeight = '700';
    btnFlow.style.cursor = 'pointer';
    btnFlow.style.boxShadow = '0 8px 24px rgba(34,211,238,0.35)';
    btnFlow.addEventListener('click', () => {
      log('Запуск UI flow теста по кнопке');
      runLocked(btnFlow, runFlow, 'Выполняется…');
    });

    wrap.appendChild(btn);
    wrap.appendChild(btnFlow);
    const btnDownload = document.createElement('button');
    btnDownload.id = 'ui-test-download';
    btnDownload.textContent = 'Скачать лог';
    btnDownload.style.padding = '10px 14px';
    btnDownload.style.borderRadius = '12px';
    btnDownload.style.border = 'none';
    btnDownload.style.background = '#0f172a';
    btnDownload.style.color = '#e2e8f0';
    btnDownload.style.fontWeight = '700';
    btnDownload.style.cursor = 'pointer';
    btnDownload.style.boxShadow = '0 8px 24px rgba(15,23,42,0.3)';
    btnDownload.addEventListener('click', () => {
      log('Кнопка скачивания лога нажата');
      try {
        downloadStoredLogs();
      } catch (err) {
        pushLog(`DOWNLOAD BUTTON ERROR: ${err?.message || err}`);
        console.error(err);
      }
      syncDownloadVisibility();
    });
    wrap.appendChild(btnDownload);
    document.body.appendChild(wrap);
    const statusLine = document.querySelector('.status-line');
    if (statusLine) {
      statusLine.appendChild(statusNode);
    } else {
      document.body.appendChild(statusNode);
    }
    syncDownloadVisibility();
  };
  createTestBtn();

  const auto = new URLSearchParams(location.search).get('autotest');
  if (auto === '1' || auto === 'true') {
    runFlow();
  }
})();
