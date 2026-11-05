"""Fixtures for the Windows E2E smoke tests.

The suite drives a real pc-remote-web.exe over HTTP and WebSocket and checks the
effect on the Windows desktop through UI Automation. If a server already
listens on PC_REMOTE_ADDR (as in the test VM), it is reused; otherwise the exe
at PC_REMOTE_EXE (default: pc-remote-web.exe in the repo root) is started.
"""

import json
import os
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

import pytest
from pywinauto import Desktop, keyboard

HERE = Path(__file__).resolve().parent
ARTIFACTS = HERE / "artifacts"
ADDR = os.environ.get("PC_REMOTE_ADDR", "127.0.0.1:8377")
EXE = Path(os.environ.get("PC_REMOTE_EXE", HERE.parent.parent / "pc-remote-web.exe"))
PASSCODE_FILE = Path(os.environ["APPDATA"]) / "pc-remote" / "passcode"
START_TIMEOUT = 20


def wait_until(condition, timeout=10.0, interval=0.2, message="condition"):
    """Poll condition() until it returns something truthy, and return that."""
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            value = condition()
            if value:
                return value
        except Exception as error:  # UIA and the clipboard fail while busy
            last_error = error
        time.sleep(interval)
    raise TimeoutError(f"{message} not met within {timeout}s (last error: {last_error!r})")


def port_open():
    host, port = ADDR.rsplit(":", 1)
    try:
        with socket.create_connection((host, int(port)), timeout=0.5):
            return True
    except OSError:
        return False


class Server:
    def __init__(self, passcode):
        self.addr = ADDR
        self.passcode = passcode

    def call(self, method, path, body=None, auth=True):
        """Send one API request and return (status code, decoded JSON body)."""
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(f"http://{self.addr}{path}", data=data, method=method)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        if auth:
            request.add_header("Authorization", f"Bearer {self.passcode}")
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, json.loads(response.read() or b"null")
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read() or b"null")


@pytest.fixture(scope="session")
def server():
    ARTIFACTS.mkdir(exist_ok=True)
    process = None
    log = None
    if not port_open():
        if not EXE.is_file():
            pytest.exit(f"{EXE} not found; build it or set PC_REMOTE_EXE", returncode=1)
        log = open(ARTIFACTS / "server.log", "wb")
        env = dict(os.environ, PC_REMOTE_ADDR=ADDR)
        process = subprocess.Popen([str(EXE)], env=env, stdout=log, stderr=subprocess.STDOUT)
        try:
            wait_until(port_open, timeout=START_TIMEOUT, message=f"server listening on {ADDR}")
        except TimeoutError:
            process.kill()
            pytest.exit(f"server did not start, see {ARTIFACTS / 'server.log'}", returncode=1)
    passcode = wait_until(
        lambda: PASSCODE_FILE.read_text(encoding="utf-8").strip(),
        timeout=START_TIMEOUT,
        message=f"passcode in {PASSCODE_FILE}",
    )
    yield Server(passcode)
    if process:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
        log.close()


def editor_text(editor):
    """Read the whole text of a Notepad editor: the classic Edit control or the
    Windows 11 RichEdit document, whichever this machine has. window_text() is
    not used: on the Windows 11 document it returns the name "Text editor"."""
    for read in (
        lambda: editor.iface_value.CurrentValue,
        lambda: editor.iface_text.DocumentRange.GetText(-1),
    ):
        try:
            text = read()
        except Exception:
            continue
        # An empty answer may only mean this pattern isn't wired up; try the next.
        if text:
            return text
    return ""


class Notepad:
    def __init__(self, window, editor):
        self.window = window
        self.editor = editor

    def focus(self):
        self.window.set_focus()
        self.editor.click_input()

    def text(self):
        return editor_text(self.editor)

    def wait_for(self, check, message):
        """Wait until check(text) holds; return the text seen."""
        return wait_until(lambda: check(self.text()) and self.text(), timeout=10, message=message)


@pytest.fixture
def notepad(request):
    known = {w.handle for w in Desktop(backend="uia").windows()}
    subprocess.Popen(["notepad.exe"])

    def new_notepad():
        for w in Desktop(backend="uia").windows(title_re=r".*Notepad$"):
            if w.handle not in known:
                return w
        return None

    window = wait_until(new_notepad, timeout=15, message="Notepad window")
    window.set_focus()
    editor = wait_until(
        lambda: next(
            iter(window.descendants(control_type="Document") + window.descendants(control_type="Edit")),
            None,
        ),
        timeout=10,
        message="Notepad editor",
    )
    pad = Notepad(window, editor)
    # Windows 11 Notepad can restore an old tab; start from an empty editor.
    pad.focus()
    keyboard.send_keys("^a{DELETE}")
    wait_until(
        lambda: pad.text().strip("\r\n") == "",
        timeout=5,
        message=f"empty editor ({editor.element_info.control_type}, text {pad.text()!r})",
    )
    yield pad

    report = getattr(request.node, "rep_call", None)
    if report is not None and report.failed:
        try:
            from PIL import ImageGrab

            ImageGrab.grab().save(ARTIFACTS / f"FAIL_{request.node.name}.png")
        except Exception:
            pass
    # Kill instead of closing so no "save changes?" dialog blocks the run.
    subprocess.run(["taskkill", "/F", "/PID", str(window.process_id())], capture_output=True)


@pytest.hookimpl(tryfirst=True, hookwrapper=True)
def pytest_runtest_makereport(item, call):
    outcome = yield
    report = outcome.get_result()
    setattr(item, f"rep_{report.when}", report)
