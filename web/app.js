const $ = (id) => document.getElementById(id);
const STORAGE_KEY = 'pc-remote-passcode';
const POLL_MS = 3000;
const STEP = 5;
const TICKS = 33;
const SHEETS = {
  reboot: { title: 'Reboot this PC?', body: 'Unsaved work in open apps may be lost.', go: 'Reboot', done: 'Rebooting' },
  shutdown: { title: 'Shut down this PC?', body: 'You won’t be able to turn it back on from your phone.', go: 'Shut Down', done: 'Shutting down' },
};

function loadPasscode() {
  try { return localStorage.getItem(STORAGE_KEY) ?? ''; } catch { return ''; }
}

function savePasscode(value) {
  try {
    if (value) localStorage.setItem(STORAGE_KEY, value);
    else localStorage.removeItem(STORAGE_KEY);
  } catch {}
}

let passcode = loadPasscode();
let volume = 0;
let muted = false;
let dragging = false;
let sendingVolume = false;
let queuedVolume = null;
let pendingSheet = null;
let nightlight = false;
let changingNight = false;
let adjustingBrightness = false;
let flashTimer = null;
let pollTimer = null;

function addTicks(fader) {
  return Array.from({ length: TICKS }, (_, i) => {
    const tick = document.createElement('span');
    tick.className = 'tick';
    tick.style.height = `${24 + (i / (TICKS - 1)) * 76}%`;
    fader.append(tick);
    return tick;
  });
}

function drawTicks(ticks, value) {
  ticks.forEach((tick, i) => tick.classList.toggle('lit', value > 0 && i / (TICKS - 1) <= value / 100));
}

const ticks = addTicks($('fader'));

async function api(path, body) {
  const response = await fetch(path, {
    method: body ? 'POST' : 'GET',
    headers: { Authorization: `Bearer ${passcode}`, ...(body && { 'Content-Type': 'application/json' }) },
    body: body && JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error ?? 'Request failed.');
    error.status = response.status;
    throw error;
  }
  return data;
}

function setState(text, online = true) {
  $('state').textContent = text;
  $('dot').classList.toggle('off', !online);
}

function flash(text) {
  clearTimeout(flashTimer);
  setState(text);
  flashTimer = setTimeout(() => { flashTimer = null; setState('Connected'); }, 2200);
}

function drawVolume() {
  const shown = Math.min(100, volume);
  $('number').textContent = shown;
  $('fader').setAttribute('aria-valuenow', shown);
  $('volume').classList.toggle('muted', muted);
  drawTicks(ticks, shown);
  $('mute').classList.toggle('on', muted);
  $('mute').setAttribute('aria-pressed', muted);
}

function drawNight() {
  $('night').classList.toggle('on', nightlight);
  $('night').setAttribute('aria-checked', nightlight);
}

let trackState = null;
let artSource = null;
let artObjectUrl = null;

async function loadArt(source) {
  if (artObjectUrl) URL.revokeObjectURL(artObjectUrl);
  artObjectUrl = null;
  $('art').hidden = true;
  $('art-empty').hidden = false;
  if (!source) return;
  let url = source;
  if (source.startsWith('/api/')) {
    // Covers come from /api/art and need the passcode, which an <img> can't send.
    try {
      const response = await fetch(source, { headers: { Authorization: `Bearer ${passcode}` } });
      if (!response.ok || source !== artSource) return;
      url = artObjectUrl = URL.createObjectURL(await response.blob());
    } catch {
      return;
    }
  }
  $('art').src = url;
}

function drawTrack(track) {
  trackState = track && { ...track, at: performance.now() };
  $('track-title').textContent = track ? track.title : 'Nothing playing';
  const player = track?.player ? track.player[0].toUpperCase() + track.player.slice(1) : '';
  $('track-sub').textContent = track ? [track.artist, player].filter(Boolean).join(' · ') : '';
  const source = track?.art || null;
  if (source !== artSource) {
    artSource = source;
    loadArt(source);
  }
  drawProgress();
}

function drawProgress() {
  let percent = 0;
  if (trackState?.length > 0) {
    const elapsed = trackState.playing ? (performance.now() - trackState.at) / 1000 : 0;
    percent = Math.min(100, ((trackState.position + elapsed) / trackState.length) * 100);
  }
  $('progress').style.width = `${percent}%`;
}

function drawOutputs(outputs) {
  const active = outputs.find((item) => item.active);
  if (active) $('output-label').textContent = active.label;
  const options = outputs.map((item) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'option';
    button.setAttribute('aria-pressed', item.active);
    const name = document.createElement('span');
    name.textContent = item.label;
    button.append(name);
    if (item.active) {
      button.insertAdjacentHTML('beforeend', '<svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>');
    }
    button.addEventListener('click', async () => {
      try {
        drawOutputs(await api('/api/output', { name: item.name }));
        closeSheets();
        flash(`Playing on ${item.label}`);
      } catch (error) {
        handleError(error);
      }
    });
    return button;
  });
  $('outputs').replaceChildren(...options);
}

function openSheet(id) {
  $(id).hidden = false;
}

function closeSheets() {
  ['output-scrim', 'type-scrim', 'clip-scrim', 'party-scrim'].forEach((id) => { $(id).hidden = true; });
  syncKeyboard();
}

function sheetOpen() {
  return ['output-scrim', 'type-scrim', 'clip-scrim', 'party-scrim'].some((id) => !$(id).hidden);
}

// Swaps in fresh stylesheets when the theme or user.css changes on the PC.
// The new link loads before the old ones go, so the page never shows unstyled,
// and the newest link always comes last, so a slower older one can't win.
let themeVersion = '';
function applyTheme(version) {
  if (!version || version === themeVersion) return;
  const first = !themeVersion;
  themeVersion = version;
  if (first) return;
  for (const id of ['theme-css', 'user-css']) {
    const links = document.querySelectorAll(`#${id}`);
    const last = links[links.length - 1];
    const link = last.cloneNode();
    link.href = `${last.href.split('?')[0]}?v=${version}`;
    link.addEventListener('load', () => {
      if (!link.isConnected) return;
      document.querySelectorAll(`#${id}`).forEach((other) => { if (other !== link) other.remove(); });
      syncThemeColor();
    }, { once: true });
    link.addEventListener('error', () => link.remove(), { once: true });
    last.after(link);
  }
}

function syncThemeColor() {
  const ground = getComputedStyle(document.documentElement).getPropertyValue('--ground').trim();
  if (ground) document.querySelector('meta[name="theme-color"]').content = ground;
}

function applyStatus(status) {
  if (status.name) $('pc-name').textContent = status.name;
  applyTheme(status.theme);
  if (!dragging && !sendingVolume) {
    volume = status.volume;
    muted = status.muted;
    drawVolume();
  }
  if (!changingNight) {
    nightlight = status.nightlight;
    drawNight();
  }
  document.querySelectorAll('[data-media]').forEach((button) => { button.disabled = !status.media; });
  $('media-note').hidden = status.media;
  $('play').setAttribute('aria-label', status.playing ? 'Pause' : 'Play');
  $('play-icon').toggleAttribute('hidden', status.playing);
  $('pause-icon').toggleAttribute('hidden', !status.playing);
  drawTrack(status.nowPlaying);
  if (status.output) $('output-label').textContent = status.output;
}

function showOffline(offline) {
  $('offline').hidden = !offline;
  $('control').hidden = offline;
  syncKeyboard();
  syncInput();
}

function showLogin(message = '') {
  clearTimeout(pollTimer);
  $('control').hidden = true;
  $('offline').hidden = true;
  $('login').hidden = false;
  $('login-error').textContent = message;
}

function handleError(error) {
  if (error.status === 401) {
    passcode = '';
    savePasscode('');
    showLogin('Enter your passcode.');
  } else if (error.status) {
    setState(error.message, false);
  } else {
    showOffline(true);
  }
}

async function poll() {
  clearTimeout(pollTimer);
  try {
    const status = await api('/api/status');
    showOffline(false);
    applyStatus(status);
    if (!flashTimer) setState('Connected');
  } catch (error) {
    handleError(error);
    if (error.status === 401) return;
  }
  if (!document.hidden) pollTimer = setTimeout(poll, POLL_MS);
}

// Sends one change at a time and keeps only the newest value while dragging.
async function sendVolume(value) {
  queuedVolume = value;
  if (sendingVolume) return;
  sendingVolume = true;
  try {
    while (queuedVolume !== null) {
      const next = queuedVolume;
      queuedVolume = null;
      const status = await api('/api/volume', { volume: next });
      if (queuedVolume === null) {
        sendingVolume = false;
        applyStatus(status);
      }
    }
  } catch (error) {
    queuedVolume = null;
    handleError(error);
  }
  sendingVolume = false;
}

function changeVolume(value) {
  volume = Math.min(100, Math.max(0, value));
  muted = false;
  drawVolume();
  sendVolume(volume);
}

function faderValue(event) {
  const box = event.currentTarget.getBoundingClientRect();
  return Math.round(((event.clientX - box.left) / box.width) * 100);
}

let screensOn = true;
let allMonitors = null;
let monitorRows = [];

function drawScreens() {
  $('screens-label').textContent = screensOn ? 'Screens off' : 'Screens on';
  $('screens').classList.toggle('on', !screensOn);
  $('screens').setAttribute('aria-pressed', !screensOn);
}

// Shows the shared slider at the monitors' level, or "Mixed" when they differ.
function drawAllMonitors() {
  if (!allMonitors) return;
  const values = monitorRows.map((row) => row.value());
  if (values.every((value) => value === values[0])) allMonitors.update(values[0]);
  else allMonitors.mixed(Math.round(values.reduce((sum, value) => sum + value, 0) / values.length));
}

function addMonitor(labelText, initial, commitValue) {
  const row = document.createElement('div');
  row.className = 'monitor';
  const head = document.createElement('div');
  head.className = 'monitor-head';
  const label = document.createElement('span');
  label.textContent = labelText;
  const reading = document.createElement('span');
  reading.className = 'monitor-value';
  head.append(label, reading);
  row.append(head);
  $('monitors').append(row);

  if (initial === null) {
    reading.textContent = 'Not available';
    return null;
  }

  let value = initial;
  const fader = document.createElement('div');
  fader.className = 'fader small';
  fader.tabIndex = 0;
  fader.setAttribute('role', 'slider');
  fader.setAttribute('aria-label', `${labelText} brightness`);
  fader.setAttribute('aria-valuemin', '1');
  fader.setAttribute('aria-valuemax', '100');
  const faderTicks = addTicks(fader);
  row.append(fader);

  const update = (next) => {
    value = Math.min(100, Math.max(1, next));
    reading.textContent = `${value}%`;
    fader.setAttribute('aria-valuenow', value);
    drawTicks(faderTicks, value);
  };
  // Sent on release, since monitors are slow to take DDC/CI writes.
  const commit = () => commitValue(value);

  update(value);
  fader.addEventListener('pointerdown', (event) => {
    adjustingBrightness = true;
    fader.setPointerCapture(event.pointerId);
    update(faderValue(event));
  });
  fader.addEventListener('pointermove', (event) => {
    if (adjustingBrightness) update(faderValue(event));
  });
  ['pointerup', 'pointercancel'].forEach((name) => {
    fader.addEventListener(name, () => {
      if (!adjustingBrightness) return;
      adjustingBrightness = false;
      commit();
    });
  });
  fader.addEventListener('keydown', (event) => {
    const delta = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1 }[event.key];
    if (delta) {
      event.preventDefault();
      update(value + delta * STEP);
      commit();
    }
  });
  return {
    row,
    update,
    value: () => value,
    mixed: (average) => {
      update(average);
      reading.textContent = 'Mixed';
    },
  };
}

async function loadBrightness() {
  if ($('monitors').childElementCount === 0) {
    const note = document.createElement('p');
    note.className = 'note';
    note.textContent = 'Reading monitors…';
    $('monitors').append(note);
  }
  try {
    const monitors = await api('/api/brightness');
    if (adjustingBrightness) return;
    $('monitors').replaceChildren();
    screensOn = monitors.every((monitor) => monitor.on);
    drawScreens();
    const available = monitors.filter((monitor) => monitor.brightness !== null);
    allMonitors = null;
    monitorRows = [];
    if (available.length > 1) {
      allMonitors = addMonitor('All monitors', available[0].brightness, async (value) => {
        monitorRows.forEach((row) => row.update(value));
        for (const monitor of available) {
          await api('/api/brightness', { monitor: monitor.name, brightness: value }).catch(handleError);
        }
      });
      allMonitors.row.classList.add('all');
    }
    monitors.forEach((monitor) => {
      const row = addMonitor(monitor.label, monitor.brightness, (value) => {
        drawAllMonitors();
        return api('/api/brightness', { monitor: monitor.name, brightness: value }).catch(handleError);
      });
      if (row) monitorRows.push(row);
    });
    drawAllMonitors();
  } catch (error) {
    $('monitors').replaceChildren();
    handleError(error);
  }
}

function showView(name) {
  ['control', 'display', 'input', 'game'].forEach((view) => {
    $(`view-${view}`).hidden = view !== name;
    if (view === name) $(`tab-${view}`).setAttribute('aria-current', 'page');
    else $(`tab-${view}`).removeAttribute('aria-current');
  });
  if (name === 'display') loadBrightness();
  syncKeyboard();
  syncInput();
  pad.sync();
  syncParty();
}

const MODIFIERS = new Set(['leftctrl', 'rightctrl', 'leftshift', 'rightshift', 'leftalt', 'leftmeta']);
const MODIFIER_NAMES = { leftctrl: 'Ctrl', rightctrl: 'Ctrl', leftshift: 'Shift', rightshift: 'Shift', leftalt: 'Alt', leftmeta: 'Super' };
const TAP_MS = 250;
const RECONNECT_MS = 1000;
const PING_MS = 1000;
const SHIFTED = {
  1: '!', 2: '@', 3: '#', 4: '$', 5: '%', 6: '^', 7: '&', 8: '*', 9: '(', 0: ')', '-': '_', '=': '+',
  '[': '{', ']': '}', '\\': '|', ';': ':', '\'': '"', ',': '<', '.': '>', '/': '?', '`': '~',
};
const key = (label, name = label, grow = 1, fn) => ({ label, name, grow, fn });
const letters = (text) => text.split('').map((letter) => key(letter));
const KEY_LAYOUT = [
  [
    key('Esc', 'esc', 1.2, ['`', 'grave']),
    key('1', '1', 1, ['F1', 'f1']), key('2', '2', 1, ['F2', 'f2']), key('3', '3', 1, ['F3', 'f3']),
    key('4', '4', 1, ['F4', 'f4']), key('5', '5', 1, ['F5', 'f5']), key('6', '6', 1, ['F6', 'f6']),
    key('7', '7', 1, ['F7', 'f7']), key('8', '8', 1, ['F8', 'f8']), key('9', '9', 1, ['F9', 'f9']),
    key('0', '0', 1, ['F10', 'f10']), key('-', 'minus', 1, ['F11', 'f11']), key('=', 'equal', 1, ['F12', 'f12']),
    key('Bksp', 'backspace', 1.5),
  ],
  [
    key('Tab', 'tab', 1.4), ...letters('qwertyuiop'),
    key('[', 'leftbrace'), key(']', 'rightbrace'), key('\\', 'backslash'), key('Del', 'delete', 1.1),
  ],
  [
    key('Caps', 'capslock', 1.7), ...letters('asdfghjkl'),
    key(';', 'semicolon'), key('\'', 'apostrophe'), key('Enter', 'enter', 2),
  ],
  [
    key('Shift', 'leftshift', 2.2), ...letters('zxcvbnm'),
    key(',', 'comma'), key('.', 'dot'), key('/', 'slash'),
    key('↑', 'up', 1, ['PgUp', 'pageup']), key('Shift', 'rightshift', 1.5),
  ],
  [
    key('Fn', null, 1.1), key('Ctrl', 'leftctrl', 1.3), key('Super', 'leftmeta', 1.3), key('Alt', 'leftalt', 1.2),
    key('Space', 'space', 5.4), key('Alt', 'leftalt', 1.1), key('Ctrl', 'rightctrl', 1.1),
    key('←', 'left', 1, ['Home', 'home']), key('↓', 'down', 1, ['PgDn', 'pagedown']), key('→', 'right', 1, ['End', 'end']),
  ],
];

// Fingers currently on a key, by pointer id. Each pointer is one finger, so
// several keys can be held at once.
const touches = new Map();
const latched = new Set();
const landscape = matchMedia('(orientation: landscape)');
let keyboardOpen = false;
let keyboardReady = false;
let keyboardSocket = null;
let keyboardPing = null;
let fnOn = false;
let lastCombo = '';
let usedWithModifier = false;

function heldNames() {
  return new Set([...latched, ...[...touches.values()].map((touch) => touch.name)]);
}

function activeModifiers() {
  const names = [...heldNames()].filter((name) => MODIFIERS.has(name)).map((name) => MODIFIER_NAMES[name]);
  return [...new Set(names)];
}

function keyFace(item, shift) {
  const label = fnOn && item.fn ? item.fn[0] : item.label;
  if (!shift) return label;
  if (SHIFTED[label]) return SHIFTED[label];
  return /^[a-z]$/.test(label) ? label.toUpperCase() : label;
}

function drawKeyboard() {
  const held = heldNames();
  const shift = held.has('leftshift') || held.has('rightshift');
  KEY_LAYOUT.flat().forEach((item) => {
    const face = keyFace(item, shift);
    const name = fnOn && item.fn ? item.fn[1] : item.name;
    item.element.textContent = face;
    item.element.classList.toggle('long', face.length > 1);
    item.element.classList.toggle('down', (!item.name && fnOn) || [...touches.values()].some((touch) => touch.name === name));
    item.element.classList.toggle('latched', latched.has(name));
  });
  const modifiers = activeModifiers();
  const waiting = modifiers.length && [...held].every((name) => MODIFIERS.has(name));
  $('combo').textContent = waiting ? `${modifiers.join(' + ')} + …` : lastCombo;
}

function sendInput(message) {
  if (keyboardReady) keyboardSocket.send(message);
}

function sendKey(action, name) {
  sendInput(`${action} ${name}`);
}

function clearKeys() {
  touches.clear();
  latched.clear();
  document.querySelectorAll('.zone.down, .strip.down').forEach((element) => element.classList.remove('down'));
  drawKeyboard();
}

function pressKey(event, item) {
  event.preventDefault();
  if (!item.name) {
    fnOn = !fnOn;
    drawKeyboard();
    return;
  }
  const name = fnOn && item.fn ? item.fn[1] : item.name;
  const modifier = MODIFIERS.has(name);
  const shift = heldNames().has('leftshift') || heldNames().has('rightshift');
  if (modifier) {
    usedWithModifier = false;
  } else {
    usedWithModifier = true;
    lastCombo = [...activeModifiers(), keyFace(item, shift)].join(' + ');
  }
  touches.set(event.pointerId, { name, at: Date.now() });
  sendKey('d', name);
  drawKeyboard();
}

function releaseKey(event) {
  const touch = touches.get(event.pointerId);
  if (!touch) return;
  touches.delete(event.pointerId);
  const { name } = touch;
  if (!MODIFIERS.has(name)) {
    sendKey('u', name);
    latched.forEach((held) => sendKey('u', held));
    latched.clear();
  } else if (latched.has(name)) {
    latched.delete(name);
    sendKey('u', name);
  } else if (Date.now() - touch.at < TAP_MS && !usedWithModifier) {
    // A quick tap keeps the modifier down for the next key.
    latched.add(name);
  } else {
    sendKey('u', name);
  }
  drawKeyboard();
}

function setKeyboardState(text, online) {
  $('keyboard-state').textContent = text;
  $('keyboard-dot').classList.toggle('off', !online);
  $('pad-hint').textContent = online ? 'Drag to move · Tap to click' : text;
}

function inputWanted() {
  return !document.hidden && !$('control').hidden && !$('view-input').hidden;
}

function syncInput() {
  if (inputWanted()) connectKeyboard();
  else if (keyboardSocket) keyboardSocket.close();
}

function connectKeyboard() {
  if (keyboardSocket || !inputWanted()) return;
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const socket = new WebSocket(`${scheme}://${location.host}/api/keyboard`);
  keyboardSocket = socket;
  setKeyboardState('Connecting', false);
  socket.onopen = () => socket.send(passcode);
  socket.onmessage = (event) => {
    if (event.data !== 'ok') return;
    keyboardReady = true;
    setKeyboardState('Live', true);
    keyboardPing = setInterval(() => socket.send('p'), PING_MS);
  };
  socket.onclose = () => {
    clearInterval(keyboardPing);
    keyboardSocket = null;
    keyboardReady = false;
    clearKeys();
    if (!inputWanted()) return;
    setKeyboardState('Reconnecting', false);
    setTimeout(connectKeyboard, RECONNECT_MS);
  };
}

function openKeyboard() {
  if (keyboardOpen) return;
  keyboardOpen = true;
  lastCombo = '';
  $('keyboard').hidden = false;
  drawKeyboard();
  syncInput();
}

function closeKeyboard() {
  keyboardOpen = false;
  $('keyboard').hidden = true;
  sendInput('r');
  clearKeys();
  syncInput();
}

const MOVE_GAIN = 1.4;
const MOVE_ACCELERATION = 0.09;
const MAX_ACCELERATION = 2;
const TWO_FINGER_SCROLL_GAIN = 7;
const STRIP_SCROLL_GAIN = 9;
const TAP_DISTANCE = 8;
const pads = new Map();
let gesture = null;
let stripY = null;
let moveX = 0;
let moveY = 0;
let scrollBy = 0;
let flushQueued = false;

// Motion is summed and sent once per frame, keeping fractions for the next one.
function flushPointer() {
  flushQueued = false;
  const x = Math.trunc(moveX);
  const y = Math.trunc(moveY);
  const scroll = Math.trunc(scrollBy);
  moveX -= x;
  moveY -= y;
  scrollBy -= scroll;
  if (x || y) sendInput(`m ${x} ${y}`);
  if (scroll) sendInput(`s ${scroll}`);
}

function queueFlush() {
  if (flushQueued) return;
  flushQueued = true;
  requestAnimationFrame(flushPointer);
}

function clickButton(name) {
  sendKey('d', name);
  sendKey('u', name);
}

function padDown(event) {
  event.preventDefault();
  $('pad').setPointerCapture(event.pointerId);
  pads.set(event.pointerId, { x: event.clientX, y: event.clientY });
  gesture ??= { fingers: 0, moved: 0, at: Date.now() };
  gesture.fingers = Math.max(gesture.fingers, pads.size);
}

function padMove(event) {
  const finger = pads.get(event.pointerId);
  if (!finger) return;
  const dx = event.clientX - finger.x;
  const dy = event.clientY - finger.y;
  finger.x = event.clientX;
  finger.y = event.clientY;
  const distance = Math.hypot(dx, dy);
  gesture.moved += distance;
  if (pads.size === 1 && gesture.fingers === 1) {
    const gain = MOVE_GAIN * (1 + Math.min(distance * MOVE_ACCELERATION, MAX_ACCELERATION));
    moveX += dx * gain;
    moveY += dy * gain;
  } else if (pads.size > 1) {
    scrollBy += (dy * TWO_FINGER_SCROLL_GAIN) / pads.size;
  }
  queueFlush();
}

function padUp(event) {
  if (!pads.delete(event.pointerId) || pads.size) return;
  const tapped = event.type === 'pointerup' && gesture.moved < TAP_DISTANCE && Date.now() - gesture.at < TAP_MS;
  if (tapped) clickButton(gesture.fingers > 1 ? 'btnright' : 'btnleft');
  gesture = null;
}

function stripEnd() {
  stripY = null;
  $('strip').classList.remove('down');
}

$('pad').addEventListener('pointerdown', padDown);
$('pad').addEventListener('pointermove', padMove);
$('pad').addEventListener('pointerup', padUp);
$('pad').addEventListener('pointercancel', padUp);
$('pad').addEventListener('contextmenu', (event) => event.preventDefault());

$('strip').addEventListener('pointerdown', (event) => {
  event.preventDefault();
  $('strip').setPointerCapture(event.pointerId);
  $('strip').classList.add('down');
  stripY = event.clientY;
});
$('strip').addEventListener('pointermove', (event) => {
  if (stripY === null) return;
  scrollBy += (event.clientY - stripY) * STRIP_SCROLL_GAIN;
  stripY = event.clientY;
  queueFlush();
});
$('strip').addEventListener('pointerup', stripEnd);
$('strip').addEventListener('pointercancel', stripEnd);

document.querySelectorAll('.zone').forEach((zone) => {
  const release = () => {
    if (!zone.classList.contains('down')) return;
    zone.classList.remove('down');
    sendKey('u', zone.dataset.button);
  };
  zone.addEventListener('pointerdown', (event) => {
    event.preventDefault();
    zone.setPointerCapture(event.pointerId);
    zone.classList.add('down');
    sendKey('d', zone.dataset.button);
  });
  zone.addEventListener('pointerup', release);
  zone.addEventListener('pointercancel', release);
  zone.addEventListener('contextmenu', (event) => event.preventDefault());
});

// On the Input tab, the way the phone is held picks the tool: upright is the
// trackpad and sideways is the keyboard.
function syncKeyboard() {
  const wanted = landscape.matches && navigator.maxTouchPoints > 0 && !$('control').hidden && !$('view-input').hidden && !sheetOpen();
  if (wanted) openKeyboard();
  else if (keyboardOpen) closeKeyboard();
}

KEY_LAYOUT.forEach((row) => {
  const rowElement = document.createElement('div');
  rowElement.className = 'key-row';
  row.forEach((item) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'k';
    button.style.setProperty('--grow', item.grow);
    button.addEventListener('pointerdown', (event) => pressKey(event, item));
    button.addEventListener('pointerup', releaseKey);
    button.addEventListener('pointercancel', releaseKey);
    item.element = button;
    rowElement.append(button);
  });
  $('key-rows').append(rowElement);
});
$('keyboard').addEventListener('contextmenu', (event) => event.preventDefault());
landscape.addEventListener('change', syncKeyboard);

// The controller is player 1 and lives on the Game tab; its layout is saved on the PC.
const pad = setupGamepad({
  socketPath: '/api/gamepad',
  credential: () => passcode,
  player: () => 1,
  isActive: () => !$('control').hidden && !$('view-game').hidden,
  canEdit: true,
  loadLayout: () => api('/api/gamepad/layout'),
  saveLayout: (layout) => api('/api/gamepad/layout', layout),
  onStatus: ({ ready, connecting, delay, peak }) => {
    $('game-player').textContent = ready ? 'Player 1' : (connecting ? 'Connecting' : 'Offline');
    $('game-delay').textContent = ready && delay !== null ? `${delay} ms · peak ${peak}` : '–';
    myDelay = ready ? delay : null;
  },
  onError: (error) => { if (error.status === 401) handleError(error); },
});
$('game-open').addEventListener('click', () => pad.open());

// ---- Party ----
// Friends on the home Wi-Fi join as players 2 to 4 with a 4-letter code, on
// the guest port, which opens only while a party runs. The list refreshes
// every two seconds while the Game tab or the party sheet is showing.
const PARTY_POLL_MS = 2000;
let party = { active: false, players: [] };
let partyTimer = null;
let myDelay = null;
let copiedTimer = null;

function partyWanted() {
  return !document.hidden && !$('control').hidden && (!$('view-game').hidden || !$('party-scrim').hidden || !$('qr-screen').hidden);
}

async function loadParty() {
  clearTimeout(partyTimer);
  try {
    party = await api('/api/party');
    drawParty();
  } catch (error) {
    handleError(error);
  }
  if (partyWanted()) partyTimer = setTimeout(loadParty, PARTY_POLL_MS);
}

async function partyAction(action, extra = {}) {
  try {
    party = await api('/api/party', { action, ...extra });
    drawParty();
    return true;
  } catch (error) {
    handleError(error);
    return false;
  }
}

function partyHost() {
  return party.link ? party.link.replace(/^https?:\/\//, '').replace(/\/#.*$/, '') : '';
}

function drawQr(rows) {
  const quiet = 4;
  const size = rows.length + quiet * 2;
  let path = '';
  rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) if (row[x] === '1') path += `M${x + quiet} ${y + quiet}h1v1h-1z`;
  });
  $('qr-svg').setAttribute('viewBox', `0 0 ${size} ${size}`);
  $('qr-svg').innerHTML = `<path d="${path}" fill="#0E0F0C" shape-rendering="crispEdges"/>`;
}

function playerRow(slot, name, detail, { you = false, empty = false } = {}) {
  const row = document.createElement('div');
  row.className = `party-player${you ? ' you' : ''}${empty ? ' empty' : ''}`;
  const badge = document.createElement('span');
  badge.className = 'party-badge';
  badge.textContent = `P${slot}`;
  const label = document.createElement('span');
  label.className = 'party-name';
  label.textContent = name;
  const info = document.createElement('span');
  info.className = 'party-delay';
  info.textContent = detail;
  row.append(badge, label, info);
  if (!you && !empty) {
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'party-remove';
    remove.textContent = 'Remove';
    remove.setAttribute('aria-label', `Remove ${name}`);
    remove.addEventListener('click', () => partyAction('remove', { slot }));
    row.append(remove);
  }
  return row;
}

function drawParty() {
  const players = party.players ?? [];
  $('invite-label').textContent = party.active ? `Party · ${party.code} · ${players.length + 1} players` : 'Invite players';
  $('invite').classList.toggle('live', party.active);
  if (!party.active) {
    if (!$('party-scrim').hidden) closeSheets();
    $('qr-screen').hidden = true;
    return;
  }
  const letters = $('party-letters');
  letters.setAttribute('aria-label', `Party code ${party.code.split('').join(' ')}`);
  letters.replaceChildren(...party.code.split('').map((letter) => {
    const cell = document.createElement('span');
    cell.className = 'party-letter';
    cell.textContent = letter;
    return cell;
  }));
  $('party-link').textContent = partyHost();
  $('party-count').textContent = `Players · ${players.length + 1} of 4`;
  const rows = [playerRow(1, 'You', myDelay !== null ? `${myDelay} ms` : '', { you: true })];
  for (let slot = 2; slot <= 4; slot++) {
    const guest = players.find((p) => p.slot === slot);
    if (guest) rows.push(playerRow(slot, guest.name, guest.connected ? (guest.delay ? `${guest.delay} ms` : '') : 'Away'));
    else rows.push(playerRow(slot, 'Waiting for a friend…', '', { empty: true }));
  }
  $('party-players').replaceChildren(...rows);
  if (party.qr) drawQr(party.qr);
  $('qr-link').textContent = partyHost();
  $('qr-code').textContent = party.code;
  const spots = 3 - players.length;
  $('qr-spots').textContent = spots > 0 ? `${spots} of 3 spots left` : 'The party is full';
}

function syncParty() {
  if (partyWanted()) loadParty();
  else clearTimeout(partyTimer);
}

function flashCopied() {
  clearTimeout(copiedTimer);
  $('party-copy-label').textContent = 'Copied';
  copiedTimer = setTimeout(() => { $('party-copy-label').textContent = 'Copy'; }, 1500);
}

// Clipboard writes need HTTPS; over plain http the link is selected for copying instead.
async function copyLink() {
  try {
    await navigator.clipboard.writeText(party.link);
    flashCopied();
  } catch {
    const range = document.createRange();
    range.selectNodeContents($('party-link'));
    getSelection().removeAllRanges();
    getSelection().addRange(range);
  }
}

$('invite').addEventListener('click', async () => {
  if (!party.active && !(await partyAction('start'))) return;
  openSheet('party-scrim');
  syncKeyboard();
  syncParty();
});
$('party-new').addEventListener('click', () => partyAction('code'));
$('party-end').addEventListener('click', () => partyAction('end'));
$('party-copy').addEventListener('click', copyLink);
$('party-share').addEventListener('click', async () => {
  if (!navigator.share) {
    copyLink();
    return;
  }
  try {
    await navigator.share({ title: 'Join the game', text: `Join the game on ${$('pc-name').textContent}. Code: ${party.code}`, url: party.link });
  } catch {}
});
$('party-qr').addEventListener('click', () => {
  $('qr-screen').hidden = false;
});
$('qr-close').addEventListener('click', () => {
  $('qr-screen').hidden = true;
});


async function action(path, body, message) {
  try {
    const status = await api(path, body);
    if (message) flash(message);
    if (path !== '/api/power') applyStatus(status);
  } catch (error) {
    handleError(error);
  }
}

function askPower(kind) {
  pendingSheet = kind;
  const sheet = SHEETS[kind];
  $('sheet-title').textContent = sheet.title;
  $('sheet-body').textContent = sheet.body;
  $('sheet-go').textContent = sheet.go;
  $('scrim').hidden = false;
  $('sheet-cancel').focus();
}

function closeSheet() {
  pendingSheet = null;
  $('scrim').hidden = true;
}

$('fader').addEventListener('pointerdown', (event) => {
  dragging = true;
  $('fader').setPointerCapture(event.pointerId);
  changeVolume(faderValue(event));
});
$('fader').addEventListener('pointermove', (event) => {
  if (dragging) changeVolume(faderValue(event));
});
['pointerup', 'pointercancel'].forEach((name) => {
  $('fader').addEventListener(name, () => { dragging = false; });
});
$('fader').addEventListener('keydown', (event) => {
  const delta = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1 }[event.key];
  if (delta) {
    event.preventDefault();
    changeVolume(volume + delta * STEP);
  }
});

$('down').addEventListener('click', () => changeVolume(volume - STEP));
$('up').addEventListener('click', () => changeVolume(volume + STEP));
$('mute').addEventListener('click', () => {
  muted = !muted;
  drawVolume();
  action('/api/mute', { muted });
});
document.querySelectorAll('[data-media]').forEach((button) => {
  button.addEventListener('click', () => action('/api/media', { action: button.dataset.media }));
});
$('retry').addEventListener('click', poll);
setInterval(drawProgress, 500);
$('art').addEventListener('load', () => {
  $('art').hidden = false;
  $('art-empty').hidden = true;
});
$('art').addEventListener('error', () => {
  $('art').hidden = true;
  $('art-empty').hidden = false;
});

$('output-button').addEventListener('click', async () => {
  openSheet('output-scrim');
  try {
    drawOutputs(await api('/api/outputs'));
  } catch (error) {
    handleError(error);
  }
});

$('screens').addEventListener('click', async () => {
  const next = !screensOn;
  try {
    await api('/api/screens', { on: next });
    screensOn = next;
    drawScreens();
    flash(next ? 'Screens on' : 'Screens off');
  } catch (error) {
    handleError(error);
  }
});

async function typeText(enter) {
  const text = $('type-text').value;
  if (!text && !enter) return;
  try {
    await api('/api/type', { text, enter });
    $('type-text').value = '';
    flash('Typed on PC');
  } catch (error) {
    handleError(error);
  }
}

$('type-button').addEventListener('click', () => {
  openSheet('type-scrim');
  $('type-text').focus();
});
$('type-send').addEventListener('click', () => typeText(false));
$('type-enter').addEventListener('click', () => typeText(true));

$('clip-button').addEventListener('click', async () => {
  openSheet('clip-scrim');
  $('clip-from').value = 'Reading…';
  try {
    const clip = await api('/api/clipboard');
    $('clip-from').value = clip.text;
    $('clip-from').placeholder = 'Nothing copied on your PC';
  } catch (error) {
    $('clip-from').value = '';
    handleError(error);
  }
});
$('clip-copy').addEventListener('click', async () => {
  const text = $('clip-from').value;
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // The Clipboard API needs HTTPS, so fall back to copying the selection.
    $('clip-from').select();
    document.execCommand('copy');
  }
  flash('Copied to phone');
});
$('clip-send').addEventListener('click', async () => {
  const text = $('clip-to').value;
  if (!text) return;
  try {
    await api('/api/clipboard', { text });
    $('clip-to').value = '';
    flash('Copied to PC');
  } catch (error) {
    handleError(error);
  }
});

document.querySelectorAll('[data-close]').forEach((button) => button.addEventListener('click', closeSheets));
['output-scrim', 'type-scrim', 'clip-scrim', 'party-scrim'].forEach((id) => {
  $(id).addEventListener('click', (event) => {
    if (event.target === $(id)) closeSheets();
  });
});
$('tab-control').addEventListener('click', () => showView('control'));
$('tab-display').addEventListener('click', () => showView('display'));
$('tab-input').addEventListener('click', () => showView('input'));
$('tab-game').addEventListener('click', () => showView('game'));
$('night').addEventListener('click', async () => {
  if (changingNight) return;
  const previous = nightlight;
  changingNight = true;
  nightlight = !previous;
  drawNight();
  try {
    await api('/api/nightlight', { enabled: nightlight });
  } catch (error) {
    nightlight = previous;
    drawNight();
    handleError(error);
  }
  changingNight = false;
});
$('lock').addEventListener('click', () => action('/api/power', { action: 'lock' }, 'Screen locked'));
$('reboot').addEventListener('click', () => askPower('reboot'));
$('shutdown').addEventListener('click', () => askPower('shutdown'));
$('sheet-cancel').addEventListener('click', closeSheet);
$('sheet-go').addEventListener('click', () => {
  const kind = pendingSheet;
  closeSheet();
  action('/api/power', { action: kind }, SHEETS[kind].done);
});

$('login-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  passcode = $('passcode').value.replace(/\s+/g, ''); // shown in groups of four
  try {
    const status = await api('/api/status');
    savePasscode(passcode);
    $('passcode').value = '';
    $('login').hidden = true;
    $('control').hidden = false;
    applyStatus(status);
    setState('Connected');
    syncKeyboard();
    pad.sync();
    pollTimer = setTimeout(poll, POLL_MS);
  } catch (error) {
    $('login-error').textContent = error.status ? error.message : 'Can’t reach the PC.';
  }
});

document.addEventListener('visibilitychange', () => {
  syncInput();
  pad.sync();
  syncParty();
  if (document.hidden || !$('login').hidden) return;
  poll();
  if (!$('view-display').hidden) loadBrightness();
});

// Keeps the page available when the PC is off. Browsers only allow this on HTTPS.
syncThemeColor();

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

if (passcode) {
  $('control').hidden = false;
  syncKeyboard();
  poll();
} else {
  showLogin();
}
