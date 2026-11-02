// The touch controller, shared by the app and the guest page. It adds its own
// overlay to the page, talks to a gamepad socket, and reports back through
// the options:
//   socketPath     the WebSocket path
//   credential()   the first message: the passcode, or a guest's token
//   player()       the player number to show
//   isActive()     whether the page wants the controller (its tab is showing)
//   canEdit        whether the layout can be edited and saved
//   loadLayout(), saveLayout(layout)   promises; omitted for guests
//   onStatus({ ready, connecting, delay }), onEnded(reason), onError(error)
// It returns { sync, open }: sync after the page changes what isActive() says,
// and open to show the controller without turning the phone.
function setupGamepad(options) {
  const $ = (id) => document.getElementById(id);
  const landscape = matchMedia('(orientation: landscape)');
  const RECONNECT_MS = 1000;
  document.body.insertAdjacentHTML('beforeend', `
<div class="gp-sizer" id="gp-sizer" aria-hidden="true"></div>
<div class="gamepad" id="gamepad" hidden>
  <div class="gp-controls" id="gp-controls"></div>
  <div class="gp-bar" id="gp-bar">
    <p class="state"><span class="dot off" id="gp-dot" aria-hidden="true"></span><span id="gp-state" aria-live="polite">Connecting</span></p>
    <button class="gp-tool" type="button" id="gp-edit" aria-label="Edit layout">
      <svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16v4z"/><path d="m13.5 6.5 4 4"/></svg>
    </button>
    <button class="gp-tool" type="button" id="gp-close" aria-label="Close controller" hidden>
      <svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12"/><path d="M18 6 6 18"/></svg>
    </button>
  </div>
  <div class="gp-toolbar" id="gp-toolbar" role="toolbar" aria-label="Edit layout" hidden>
    <button type="button" class="gp-reset" id="gp-reset">Reset</button>
    <p class="gp-hint" id="gp-hint">Drag to move · tap to resize</p>
    <div class="gp-selected" id="gp-selected" hidden>
      <span id="gp-name"></span>
      <input type="range" id="gp-size" min="60" max="160" step="5" aria-label="Size">
      <button type="button" id="gp-hide">Hide</button>
    </div>
    <button type="button" class="gp-done" id="gp-done">Done</button>
  </div>
</div>
`);

  // The phone sends the whole controller state ("g buttons lx ly rx ry lt rt") on
  // every change and four times a second, so a lost message is fixed by the next.
  // Layouts are drawn on an 844×390 reference screen and stored as shares of the
  // screen, so they fit any phone; sizes scale with the screen.
  const PAD_W = 844;
  const PAD_H = 390;
  const PAD_SNAP = 8;
  const PAD_RESEND_MS = 250;
  const PAD_DELAY_MS = 2000;
  const STICK_MAX = 32767;
  const PAD_BITS = { a: 0, b: 1, x: 2, y: 3, lb: 4, rb: 5, back: 6, start: 7, home: 8, l3: 9, r3: 10 };
  const DPAD_BITS = { up: 11, down: 12, left: 13, right: 14 };
  const PAD_ICONS = {
    back: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
    home: '<path d="M4 11 12 4l8 7"/><path d="M6 10v9h12v-9"/>',
    start: '<path d="M5 7h14"/><path d="M5 12h14"/><path d="M5 17h14"/>',
  };
  const PAD_CONTROLS = [
    { id: 'lt', name: 'LT', spoken: 'Left trigger', w: 84, h: 44, x: 86, y: 34, look: 'lt' },
    { id: 'lb', name: 'LB', spoken: 'Left bumper', w: 84, h: 44, x: 178, y: 34 },
    { id: 'rb', name: 'RB', spoken: 'Right bumper', w: 84, h: 44, x: 666, y: 34 },
    { id: 'rt', name: 'RT', spoken: 'Right trigger', w: 84, h: 44, x: 758, y: 34, look: 'rt' },
    { id: 'ls', name: 'Left stick', spoken: 'Left stick', kind: 'stick', w: 224, h: 306, x: 136, y: 221, base: 112, knob: 56, hint: 'Touch anywhere' },
    { id: 'dpad', name: 'D-pad', spoken: 'D-pad', kind: 'dpad', w: 124, h: 124, x: 318, y: 300 },
    { id: 'back', name: 'Back', spoken: 'Back', icon: 'back', w: 44, h: 44, x: 374, y: 126, look: 'round' },
    { id: 'home', name: 'Home', spoken: 'Home', icon: 'home', w: 52, h: 52, x: 422, y: 118, look: 'round' },
    { id: 'start', name: 'Start', spoken: 'Start', icon: 'start', w: 44, h: 44, x: 470, y: 126, look: 'round' },
    { id: 'l3', name: 'L3', spoken: 'Left stick press', w: 48, h: 32, x: 392, y: 176, look: 'small' },
    { id: 'r3', name: 'R3', spoken: 'Right stick press', w: 48, h: 32, x: 452, y: 176, look: 'small' },
    { id: 'rs', name: 'Right stick', spoken: 'Right stick', kind: 'stick', w: 156, h: 160, x: 542, y: 294, base: 88, knob: 44, hint: 'Right stick' },
    { id: 'y', name: 'Y', spoken: 'Y', w: 60, h: 60, x: 710, y: 92, look: 'face' },
    { id: 'x', name: 'X', spoken: 'X', w: 60, h: 60, x: 648, y: 154, look: 'face' },
    { id: 'b', name: 'B', spoken: 'B', w: 60, h: 60, x: 772, y: 154, look: 'face' },
    { id: 'a', name: 'A', spoken: 'A', w: 60, h: 60, x: 710, y: 216, look: 'face' },
  ];
  const PAD_BY_ID = Object.fromEntries(PAD_CONTROLS.map((c) => [c.id, c]));

  function defaultPadLayout() {
    return Object.fromEntries(PAD_CONTROLS.map((c) => [c.id, { x: (c.x / PAD_W) * 100, y: (c.y / PAD_H) * 100, size: 100, hidden: false }]));
  }

  let padLayout = defaultPadLayout();
  let savedPadLayout = padLayout;
  let padLayoutLoaded = false;
  let padOpen = false;
  let padForced = false;
  let padDismissed = false;
  let padTurned = false;
  let padSize = { w: PAD_W, h: PAD_H };
  let padEditing = false;
  let padSelected = null;
  let padDrag = null;
  let padSocket = null;
  let padReady = false;
  let padResend = null;
  let padPing = null;
  let padDelay = null;
  let padFrame = 0;
  let padFlash = '';
  let padFlashTimer = null;
  const padPointers = new Map();
  const padSticks = {};
  let padDpad = { pointer: null, dirs: {} };
  const padElements = new Map();

  const buzz = () => navigator.vibrate?.(8);

  function padScale(id) {
    return Math.min(padSize.w / PAD_W, padSize.h / PAD_H) * (padLayout[id].size / 100);
  }

  // Turned is drawn sideways on an upright screen (rotation lock), so screen
  // coordinates are rotated back into the controller's own.
  function toPad(event) {
    if (padTurned) return { x: event.clientY, y: padSize.h - event.clientX };
    return { x: event.clientX, y: event.clientY };
  }

  // Where a control is drawn, kept fully on screen even if its layout was saved
  // on a bigger phone.
  function padCenter(control) {
    const spot = padLayout[control.id];
    const k = padScale(control.id);
    const halfW = Math.min((control.w * k) / 2, padSize.w / 2);
    const halfH = Math.min((control.h * k) / 2, padSize.h / 2);
    return {
      x: Math.min(Math.max((spot.x / 100) * padSize.w, halfW), padSize.w - halfW),
      y: Math.min(Math.max((spot.y / 100) * padSize.h, halfH), padSize.h - halfH),
    };
  }

  function toControl(control, event) {
    const p = toPad(event);
    const center = padCenter(control);
    const k = padScale(control.id);
    return {
      x: (p.x - center.x) / k + control.w / 2,
      y: (p.y - center.y) / k + control.h / 2,
    };
  }

  function padMessage() {
    const held = new Set(padPointers.values());
    let bits = 0;
    for (const [id, bit] of Object.entries(PAD_BITS)) if (held.has(id)) bits |= 1 << bit;
    for (const [dir, bit] of Object.entries(DPAD_BITS)) if (padDpad.dirs[dir]) bits |= 1 << bit;
    const axis = (id, key) => {
      const stick = padSticks[id];
      return stick ? Math.round((stick[key] / (PAD_BY_ID[id].base / 2)) * STICK_MAX) : 0;
    };
    const trigger = (id) => (held.has(id) ? 255 : 0);
    return `g ${bits} ${axis('ls', 'dx')} ${axis('ls', 'dy')} ${axis('rs', 'dx')} ${axis('rs', 'dy')} ${trigger('lt')} ${trigger('rt')}`;
  }

  function sendPad() {
    if (padReady) padSocket.send(padMessage());
  }

  function schedulePad() {
    if (!padFrame) padFrame = requestAnimationFrame(() => { padFrame = 0; sendPad(); });
  }

  // Eight directions from where the finger is on the pad; the middle is neutral.
  function dpadDirs(control, event) {
    const p = toControl(control, event);
    const dx = p.x - control.w / 2;
    const dy = p.y - control.h / 2;
    if (Math.hypot(dx, dy) < 14) return {};
    const angle = (Math.atan2(dy, dx) * 180) / Math.PI;
    const near = (target) => Math.abs(((angle - target + 540) % 360) - 180) < 67.5;
    return { right: near(0), down: near(90), left: near(180), up: near(-90) };
  }

  function releasePad() {
    padPointers.clear();
    delete padSticks.ls;
    delete padSticks.rs;
    padDpad = { pointer: null, dirs: {} };
    PAD_CONTROLS.forEach(drawControl);
    schedulePad();
  }

  function buildPad() {
    PAD_CONTROLS.forEach((control) => {
      const el = document.createElement('button');
      el.type = 'button';
      el.className = `gp-c ${control.look ?? ''} ${control.kind === 'stick' ? 'zone' : ''} ${control.kind ?? ''}`;
      el.style.width = `${control.w}px`;
      el.style.height = `${control.h}px`;
      el.setAttribute('aria-label', control.spoken);
      if (control.icon) {
        el.innerHTML = `<svg class="i" viewBox="0 0 24 24" aria-hidden="true">${PAD_ICONS[control.icon]}</svg>`;
      } else if (control.kind === 'stick') {
        const ring = (control.w - control.base) / 2;
        el.innerHTML = `<span class="gp-ring" style="left:${ring}px;top:${(control.h - control.base) / 2 - 12}px;width:${control.base}px;height:${control.base}px"></span>`
          + `<span class="gp-zone-hint" style="top:${(control.h + control.base) / 2}px"></span>`
          + `<span class="gp-base" style="width:${control.base}px;height:${control.base}px"></span>`
          + `<span class="gp-knob" style="width:${control.knob}px;height:${control.knob}px"></span>`;
      } else if (control.kind === 'dpad') {
        const arrow = '<svg class="i" viewBox="0 0 24 24"><path d="m6 15 6-6 6 6"/></svg>';
        el.innerHTML = ['up', 'right', 'down', 'left'].map((dir) => `<span class="gp-arm ${dir}">${arrow}</span>`).join('') + '<span class="gp-hub"></span>';
      } else {
        el.textContent = control.name;
      }
      el.addEventListener('pointerdown', (event) => padDown(control, el, event));
      el.addEventListener('pointermove', (event) => padMove(control, event));
      el.addEventListener('pointerup', (event) => padUp(control, event));
      el.addEventListener('pointercancel', (event) => padUp(control, event));
      el.addEventListener('contextmenu', (event) => event.preventDefault());
      padElements.set(control.id, el);
      $('gp-controls').append(el);
    });
  }

  function padDown(control, el, event) {
    event.preventDefault();
    el.setPointerCapture(event.pointerId);
    if (padEditing) {
      editDown(control, event);
      return;
    }
    padPointers.set(event.pointerId, control.id);
    if (control.kind === 'stick') {
      const p = toControl(control, event);
      padSticks[control.id] = { pointer: event.pointerId, ox: p.x, oy: p.y, dx: 0, dy: 0 };
    } else if (control.kind === 'dpad') {
      padDpad = { pointer: event.pointerId, dirs: dpadDirs(control, event) };
    }
    buzz();
    drawControl(control);
    schedulePad();
  }

  function padMove(control, event) {
    if (padEditing) {
      editMove(control, event);
      return;
    }
    if (control.kind === 'stick') {
      const stick = padSticks[control.id];
      if (!stick || stick.pointer !== event.pointerId) return;
      const p = toControl(control, event);
      let dx = p.x - stick.ox;
      let dy = p.y - stick.oy;
      const travel = control.base / 2;
      const distance = Math.hypot(dx, dy);
      if (distance > travel) {
        dx = (dx * travel) / distance;
        dy = (dy * travel) / distance;
      }
      stick.dx = dx;
      stick.dy = dy;
    } else if (control.kind === 'dpad') {
      if (padDpad.pointer !== event.pointerId) return;
      const dirs = dpadDirs(control, event);
      const key = (d) => Object.keys(d).filter((k) => d[k]).join();
      if (key(dirs) === key(padDpad.dirs)) return;
      if (key(dirs)) buzz();
      padDpad.dirs = dirs;
    } else {
      return;
    }
    drawControl(control);
    schedulePad();
  }

  function padUp(control, event) {
    if (padEditing) {
      padDrag = null;
      return;
    }
    if (!padPointers.has(event.pointerId)) return;
    padPointers.delete(event.pointerId);
    if (padSticks[control.id]?.pointer === event.pointerId) delete padSticks[control.id];
    if (padDpad.pointer === event.pointerId) padDpad = { pointer: null, dirs: {} };
    drawControl(control);
    schedulePad();
  }

  function drawControl(control) {
    const el = padElements.get(control.id);
    if (!el) return;
    const spot = padLayout[control.id];
    const { x, y } = padCenter(control);
    el.style.left = `${x}px`;
    el.style.top = `${y}px`;
    el.style.setProperty('--k', padScale(control.id));
    el.hidden = spot.hidden && !padEditing;
    el.classList.toggle('faded', spot.hidden && padEditing);
    el.classList.toggle('selected', padEditing && padSelected === control.id);
    el.setAttribute('aria-label', padEditing ? `${control.name}. Drag to move.` : control.spoken);
    if (control.kind === 'stick') {
      const stick = padEditing ? null : padSticks[control.id];
      el.classList.toggle('active', !!stick);
      el.querySelector('.gp-zone-hint').textContent = padEditing ? control.name : control.hint;
      if (stick) {
        const base = el.querySelector('.gp-base').style;
        const knob = el.querySelector('.gp-knob').style;
        base.left = `${stick.ox - control.base / 2}px`;
        base.top = `${stick.oy - control.base / 2}px`;
        knob.left = `${stick.ox + stick.dx - control.knob / 2}px`;
        knob.top = `${stick.oy + stick.dy - control.knob / 2}px`;
      }
    } else if (control.kind === 'dpad') {
      el.querySelectorAll('.gp-arm').forEach((arm) => {
        const dir = arm.classList[1];
        arm.classList.toggle('on', !padEditing && !!padDpad.dirs[dir]);
      });
    } else {
      el.classList.toggle('on', !padEditing && [...padPointers.values()].includes(control.id));
    }
  }

  function drawPad() {
    $('gamepad').classList.toggle('editing', padEditing);
    PAD_CONTROLS.forEach(drawControl);
    $('gp-bar').hidden = padEditing;
    $('gp-edit').hidden = !options.canEdit;
    $('gp-close').hidden = !padForced;
    $('gp-toolbar').hidden = !padEditing;
    const chosen = padSelected && PAD_BY_ID[padSelected];
    $('gp-hint').hidden = !!chosen;
    $('gp-selected').hidden = !chosen;
    // The toolbar moves to the bottom when the chosen control is near the top.
    $('gp-toolbar').classList.toggle('low', !!chosen && padLayout[padSelected].y < 30);
    if (chosen) {
      $('gp-name').textContent = chosen.name;
      $('gp-size').value = padLayout[padSelected].size;
      $('gp-size').setAttribute('aria-label', `Size of ${chosen.name}`);
      $('gp-hide').textContent = padLayout[padSelected].hidden ? 'Show' : 'Hide';
    }
    drawPadStatus();
  }

  function drawPadStatus() {
    const delay = padDelay === null ? '' : ` · ${Math.round(padDelay)} ms`;
    const live = padReady ? `Player ${options.player()}${delay}` : (padSocket ? 'Connecting' : 'Offline');
    $('gp-state').textContent = padFlash || live;
    $('gp-dot').classList.toggle('off', !padReady);
    options.onStatus?.({ ready: padReady, connecting: !!padSocket, delay: padDelay === null ? null : Math.round(padDelay) });
  }

  function flashPad(text) {
    clearTimeout(padFlashTimer);
    padFlash = text;
    drawPadStatus();
    padFlashTimer = setTimeout(() => { padFlash = ''; drawPadStatus(); }, 1800);
  }

  // ---- Editing the layout ----

  function editDown(control, event) {
    const center = padCenter(control);
    padDrag = { id: control.id, pointer: event.pointerId, start: toPad(event), x: center.x, y: center.y, moved: false };
    if (padSelected !== control.id) buzz();
    padSelected = control.id;
    drawPad();
  }

  // Moves snap to an 8 px grid and keep the control fully on screen.
  function editMove(control, event) {
    if (!padDrag || padDrag.pointer !== event.pointerId) return;
    const p = toPad(event);
    const dx = p.x - padDrag.start.x;
    const dy = p.y - padDrag.start.y;
    if (!padDrag.moved && Math.hypot(dx, dy) < 4) return;
    padDrag.moved = true;
    const k = padScale(control.id);
    const halfW = (control.w * k) / 2;
    const halfH = (control.h * k) / 2;
    const snap = (v) => Math.round(v / PAD_SNAP) * PAD_SNAP;
    const x = Math.min(Math.max(snap(padDrag.x + dx), halfW), padSize.w - halfW);
    const y = Math.min(Math.max(snap(padDrag.y + dy), halfH), padSize.h - halfH);
    padLayout = { ...padLayout, [control.id]: { ...padLayout[control.id], x: (x / padSize.w) * 100, y: (y / padSize.h) * 100 } };
    drawControl(control);
    drawPad();
  }

  function updateSelected(change) {
    if (!padSelected) return;
    padLayout = { ...padLayout, [padSelected]: { ...padLayout[padSelected], ...change } };
    drawPad();
  }

  function startEditing() {
    releasePad();
    padEditing = true;
    padSelected = null;
    $('gp-hint').textContent = 'Drag to move · tap to resize';
    drawPad();
  }

  function stopEditing() {
    padEditing = false;
    padSelected = null;
    padDrag = null;
    drawPad();
  }

  async function finishEditing() {
    try {
      await options.saveLayout({ version: 1, controls: padLayout });
      savedPadLayout = padLayout;
      stopEditing();
      flashPad('Layout saved');
    } catch (error) {
      options.onError?.(error);
      padSelected = null;
      drawPad();
      $('gp-hint').textContent = 'Couldn’t save · try again';
    }
  }

  async function loadPadLayout() {
    if (padLayoutLoaded || !options.loadLayout) return;
    try {
      const saved = await options.loadLayout();
      padLayoutLoaded = true;
      const merged = defaultPadLayout();
      for (const [id, spot] of Object.entries(saved.controls ?? {})) if (merged[id]) merged[id] = spot;
      savedPadLayout = merged;
      if (!padEditing) padLayout = merged;
      drawPad();
    } catch (error) {
      options.onError?.(error);
    }
  }

  // ---- Connection ----

  function padWanted() {
    return !document.hidden && (options.isActive() || padOpen);
  }

  function syncPadSocket() {
    if (padWanted()) connectPad();
    else if (padSocket) padSocket.close();
  }

  function connectPad() {
    if (padSocket || !padWanted()) return;
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    const socket = new WebSocket(`${scheme}://${location.host}${options.socketPath}`);
    padSocket = socket;
    drawPadStatus();
    socket.onopen = () => socket.send(options.credential());
    socket.onmessage = (event) => {
      if (event.data === 'ok') {
        padReady = true;
        padResend = setInterval(sendPad, PAD_RESEND_MS);
        padPing = setInterval(() => socket.send(`t ${performance.now()}`), PAD_DELAY_MS);
        socket.send(`t ${performance.now()}`);
        sendPad();
        drawPadStatus();
        loadPadLayout();
      } else if (event.data.startsWith('t ')) {
        const ms = performance.now() - Number(event.data.slice(2));
        padDelay = padDelay === null ? ms : padDelay * 0.7 + ms * 0.3;
        socket.send(`l ${Math.round(padDelay)}`);
        drawPadStatus();
      }
    };
    socket.onclose = (event) => {
      clearInterval(padResend);
      clearInterval(padPing);
      padSocket = null;
      padReady = false;
      padDelay = null;
      releasePad();
      drawPadStatus();
      // 4000: the host removed this player or ended the party. Don't come back.
      if (event.code === 4000) {
        padForced = false;
        syncGamepad();
        options.onEnded?.(event.reason);
        return;
      }
      if (padWanted()) setTimeout(connectPad, RECONNECT_MS);
    };
  }

  // ---- Opening and closing ----

  // The screen area comes from #gp-sizer, a fixed full-screen box watched by a
  // ResizeObserver. Window sizes read during a rotation can be stale on iOS; the
  // observer reports again once the new size has settled.
  function measurePad() {
    const pad = $('gamepad');
    const screenBox = $('gp-sizer').getBoundingClientRect();
    const width = Math.round(screenBox.width);
    const height = Math.round(screenBox.height);
    padSize = padTurned ? { w: height, h: width } : { w: width, h: height };
    pad.classList.toggle('turned', padTurned);
    pad.style.width = padTurned ? `${padSize.w}px` : '';
    pad.style.height = padTurned ? `${padSize.h}px` : '';
    pad.style.left = padTurned ? `${width}px` : '';
  }

  // On the Game tab, turning the phone sideways opens the controller. The Open
  // button does too, drawn sideways when rotation lock keeps the screen upright,
  // and only then is there a close button, since turning back wouldn't work.
  function syncGamepad() {
    const onGame = options.isActive() && !document.hidden;
    if (!onGame) padForced = false;
    const sideways = landscape.matches && navigator.maxTouchPoints > 0;
    const wanted = onGame && ((sideways && !padDismissed) || padForced);
    if (wanted) {
      padOpen = true;
      padTurned = !landscape.matches;
      $('gamepad').hidden = false;
      measurePad();
      drawPad();
    } else if (padOpen) {
      if (padEditing) {
        padLayout = savedPadLayout;
        stopEditing();
      }
      releasePad();
      padOpen = false;
      $('gamepad').hidden = true;
    }
    syncPadSocket();
  }

  function openForced() {
    padForced = true;
    padDismissed = false;
    syncGamepad();
  }

  buildPad();
  $('gp-close').addEventListener('click', () => {
    padForced = false;
    padDismissed = true;
    syncGamepad();
  });
  $('gp-edit').addEventListener('click', startEditing);
  $('gp-done').addEventListener('click', finishEditing);
  $('gp-reset').addEventListener('click', () => {
    padLayout = defaultPadLayout();
    padSelected = null;
    drawPad();
  });
  $('gp-size').addEventListener('input', (event) => updateSelected({ size: Number(event.target.value) }));
  $('gp-hide').addEventListener('click', () => updateSelected({ hidden: !padLayout[padSelected].hidden }));
  $('gamepad').addEventListener('pointerdown', (event) => {
    if (padEditing && (event.target === $('gamepad') || event.target === $('gp-controls'))) {
      padSelected = null;
      drawPad();
    }
  });
  $('gamepad').addEventListener('contextmenu', (event) => event.preventDefault());
  landscape.addEventListener('change', () => {
    padDismissed = false;
    syncGamepad();
  });
  new ResizeObserver(() => {
    if (!padOpen) return;
    measurePad();
    drawPad();
  }).observe($('gp-sizer'));

  return { sync: syncGamepad, open: openForced };
}
