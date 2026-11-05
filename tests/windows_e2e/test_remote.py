"""Windows smoke tests: each API call must reach the real desktop."""

import pytest
import win32clipboard
from websockets.sync.client import connect

from conftest import wait_until


def native_clipboard():
    win32clipboard.OpenClipboard()
    try:
        return win32clipboard.GetClipboardData(win32clipboard.CF_UNICODETEXT)
    finally:
        win32clipboard.CloseClipboard()


def set_native_clipboard(text):
    win32clipboard.OpenClipboard()
    try:
        win32clipboard.EmptyClipboard()
        win32clipboard.SetClipboardData(win32clipboard.CF_UNICODETEXT, text)
    finally:
        win32clipboard.CloseClipboard()


def test_status(server):
    code, body = server.call("GET", "/api/status")
    assert code == 200, body
    assert set(body["features"]) == {
        "power", "screens", "nightlight", "brightness", "outputs",
        "nowPlaying", "gamepad", "zoom", "party",
    }


def test_status_needs_passcode(server):
    code, _ = server.call("GET", "/api/status", auth=False)
    assert code == 401


def test_type_unicode_and_enter(server, notepad):
    notepad.focus()
    code, body = server.call("POST", "/api/type", {"text": "héllo ğüş", "enter": True})
    assert code == 200, body
    # Classic Notepad ends the line with \r\n, Windows 11 Notepad with \r.
    text = notepad.wait_for(lambda t: t.endswith(("\n", "\r")), "Enter in Notepad")
    assert text.rstrip("\r\n") == "héllo ğüş"


def test_keyboard_socket_shift(server, notepad):
    notepad.focus()
    with connect(f"ws://{server.addr}/api/keyboard", open_timeout=10) as socket:
        socket.send(server.passcode)
        assert socket.recv(timeout=10) == "ok"
        for message in ("d leftshift", "d a", "u a", "u leftshift"):
            socket.send(message)
        assert notepad.wait_for(lambda t: t != "", "key in Notepad") == "A"


def test_clipboard_write_uses_crlf(server):
    code, body = server.call("POST", "/api/clipboard", {"text": "one\ntwo"})
    assert code == 200, body
    assert wait_until(native_clipboard, message="clipboard text") == "one\r\ntwo"
    code, body = server.call("GET", "/api/clipboard")
    assert code == 200
    assert body == {"text": "one\ntwo", "truncated": False}


def test_clipboard_read_drops_cr(server):
    wait_until(lambda: set_native_clipboard("three\r\nfour") or True, message="set clipboard")
    code, body = server.call("GET", "/api/clipboard")
    assert code == 200
    assert body["text"] == "three\nfour"


def test_volume(server):
    code, status = server.call("GET", "/api/status")
    assert code == 200
    code, body = server.call("POST", "/api/volume", {"volume": 30})
    if code == 500:
        pytest.skip("no audio endpoint on this machine")
    assert code == 200, body
    try:
        assert body["volume"] == 30
        code, body = server.call("GET", "/api/status")
        assert body["volume"] == 30
    finally:
        server.call("POST", "/api/volume", {"volume": status["volume"]})
