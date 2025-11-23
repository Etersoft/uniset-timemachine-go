(() => {
  const log = (msg) => console.log(`[UI TEST] ${msg}`);
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
  const createTestBtn = () => {
    if (document.getElementById('ui-test-runner')) return;
    const wrap = document.createElement('div');
    wrap.id = 'ui-test-runner';
    wrap.style.position = 'fixed';
    wrap.style.top = '12px';
    wrap.style.right = '12px';
    wrap.style.zIndex = '9999';
    wrap.style.display = 'flex';
    wrap.style.flexDirection = 'column';
    wrap.style.gap = '8px';

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
      run();
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
      runFlow();
    });

    wrap.appendChild(btn);
    wrap.appendChild(btnFlow);
    document.body.appendChild(wrap);
  };
  createTestBtn();

  const auto = new URLSearchParams(location.search).get('autotest');
  if (auto === '1' || auto === 'true') {
    runFlow();
  }
})();
