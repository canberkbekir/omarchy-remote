// The tray icon's "Show passcode" page. The tray opens it with a one-time
// ticket after the #; the page trades it for the app's address and passcode.
// The passcode stays hidden until asked for and is never stored.
const $ = (id) => document.getElementById(id);

function drawQr(rows) {
  const quiet = 4;
  const size = rows.length + quiet * 2;
  const ns = 'http://www.w3.org/2000/svg';
  let d = '';
  rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) if (row[x] === '1') d += `M${x + quiet} ${y + quiet}h1v1h-1z`;
  });
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', d);
  path.setAttribute('fill', '#0E0F0C');
  path.setAttribute('shape-rendering', 'crispEdges');
  $('qr-svg').setAttribute('viewBox', `0 0 ${size} ${size}`);
  $('qr-svg').replaceChildren(path);
}

// Groups of four, like the bar card, so it's easy to type on the phone.
function grouped(passcode) {
  return passcode.match(/.{1,4}/g)?.join(' ') ?? '';
}

async function load() {
  const ticket = location.hash.slice(1);
  // The ticket works once and only for a minute, so the copy the browser
  // already put in its history is spent by the time anyone could read it.
  // Still, keep it out of the address bar.
  history.replaceState(null, '', location.pathname);
  let info;
  try {
    const response = await fetch('/api/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ticket }),
      cache: 'no-store',
    });
    info = await response.json();
    if (!response.ok) throw new Error(info.error || response.statusText);
  } catch (err) {
    $('hint').textContent = err.message || 'Couldn’t reach the app. Open this page again from the tray icon.';
    return;
  }
  document.title = `Connect your phone to ${info.name}`;
  $('title').textContent = info.name;
  $('hint').textContent = 'Scan this with your phone’s camera';
  if (info.qr?.length) {
    drawQr(info.qr);
    $('qr-card').hidden = false;
  }
  $('label').textContent = info.label;
  $('url').textContent = info.url;
  $('passcode').textContent = grouped(info.passcode);
  $('fallback').hidden = false;
  $('reveal').addEventListener('click', () => {
    const hidden = !$('passcode').hidden;
    $('passcode').hidden = hidden;
    $('reveal').firstElementChild.textContent = hidden ? 'Show passcode' : 'Passcode';
  });
}

load();
