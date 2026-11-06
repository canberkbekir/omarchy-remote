// The guest page: join with the host's code, then play as player 2, 3 or 4.
// After joining, the token is kept on this phone, so reopening the page or
// reconnecting keeps the same player until the host ends the party.
const $ = (id) => document.getElementById(id);
const STORAGE_KEY = 'pc-remote-guest';
const CODE_LETTERS = /[^A-HJ-NP-Z]/g; // no I or O, like the codes themselves
const ENDED = {
  removed: 'The host removed you from the game.',
  ended: 'The host ended the game.',
};

function loadGuest() {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY) ?? 'null');
  } catch {
    return null;
  }
}

// A guest's controller layout stays on their own phone, so each friend can
// arrange the buttons their way without touching the host's layout.
const LAYOUT_KEY = 'pc-remote-guest-layout';

async function loadGuestLayout() {
  try {
    return JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? '{}') ?? {};
  } catch {
    return {};
  }
}

async function saveGuestLayout(layout) {
  localStorage.setItem(LAYOUT_KEY, JSON.stringify(layout)); // throws when storage is off, which shows "Couldn't save"
}

function saveGuest(value) {
  try {
    if (value) localStorage.setItem(STORAGE_KEY, JSON.stringify(value));
    else localStorage.removeItem(STORAGE_KEY);
  } catch {}
}

let guest = loadGuest();

function setHelp(text, problem = false) {
  $('code-help').textContent = text;
  $('code-help').classList.toggle('problem', problem);
  $('code').classList.toggle('problem', problem);
}

function syncJoinButton() {
  $('join-button').disabled = $('code').value.length !== 4 || !$('name').value.trim();
}

function showJoin(message) {
  $('joined').hidden = true;
  $('join').hidden = false;
  if (message) setHelp(message, true);
  syncJoinButton();
  pad.sync();
}

function showJoined() {
  $('join').hidden = true;
  $('joined').hidden = false;
  $('joined-badge').textContent = `P${guest.slot}`;
  $('joined-title').textContent = `You’re Player ${guest.slot}`;
  $('joined-hello').textContent = `Hi ${guest.name}! Turn your phone sideways to play.`;
  pad.sync();
}

const pad = setupGamepad({
  socketPath: '/api/play',
  credential: () => guest?.token ?? '',
  player: () => guest?.slot ?? '',
  isActive: () => !!guest && !$('joined').hidden,
  canEdit: true,
  loadLayout: loadGuestLayout,
  saveLayout: saveGuestLayout,
  onStatus: ({ ready, connecting }) => {
    $('joined-status').textContent = ready ? 'Connected' : (connecting ? 'Connecting' : 'Offline');
    $('joined-dot').classList.toggle('off', !ready);
  },
  onEnded: (reason) => {
    if (!guest) return; // this phone left on its own
    guest = null;
    saveGuest(null);
    $('code').value = '';
    showJoin(ENDED[reason] ?? ENDED.ended);
  },
});

$('code').addEventListener('input', () => {
  $('code').value = $('code').value.toUpperCase().replace(CODE_LETTERS, '').slice(0, 4);
  setHelp('4 letters. Ask the host for it.');
  syncJoinButton();
});
$('name').addEventListener('input', syncJoinButton);

$('join').addEventListener('submit', async (event) => {
  event.preventDefault();
  $('join-button').disabled = true;
  try {
    const response = await fetch('/api/join', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: $('code').value, name: $('name').value }),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      setHelp(data.error ?? 'Couldn’t join. Try again.', true);
      syncJoinButton();
      return;
    }
    guest = { token: data.token, slot: data.slot, name: data.name };
    saveGuest(guest);
    showJoined();
  } catch {
    setHelp('Can’t reach the PC. Are you on the same Wi-Fi?', true);
    syncJoinButton();
  }
});

$('joined-open').addEventListener('click', () => pad.open());
$('leave').addEventListener('click', () => {
  const token = guest?.token;
  guest = null;
  saveGuest(null);
  if (token) fetch('/api/leave', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) }).catch(() => {});
  showJoin();
});
document.addEventListener('visibilitychange', () => pad.sync());

// A QR code or shared link carries the code after the #. It's filled in, then
// taken out of the address so it isn't left in the browser's history.
const linkCode = decodeURIComponent(location.hash.slice(1)).toUpperCase().replace(CODE_LETTERS, '').slice(0, 4);
if (linkCode) {
  $('code').value = linkCode;
  history.replaceState(null, '', location.pathname);
}
fetch('/api/info').then((r) => r.json()).then((info) => {
  $('join-pc').textContent = `on ${info.pc}`;
  if (!info.party && !guest) setHelp('There’s no game right now. Ask the host to start one.', true);
}).catch(() => {});

if (guest) showJoined();
else showJoin();
