#!/usr/bin/env python3
"""Execute the actual native binary in a PTY with an isolated home and PATH."""
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix="devclean-runtime-") as temporary:
    home = Path(temporary).resolve()
    (home / "empty-path").mkdir()
    env = dict(os.environ, HOME=str(home), PATH=str(home / "empty-path"), TERM="xterm-256color")
    env.pop("XDG_CONFIG_HOME", None)
    derived = home / "Library/Developer/Xcode/DerivedData/Native Fixture"
    support = home / "Library/Developer/Xcode/iOS DeviceSupport/18.0 (22A100)"
    for path in (derived, support):
        path.mkdir(parents=True)
        (path / "fixture-cache").write_bytes(b"native-fixture" * 4096)
    scan = subprocess.Popen([binary, "--scan"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
    try:
        scan_out, scan_error = scan.communicate(timeout=3)
    except subprocess.TimeoutExpired:
        # A standard system simulator tree can be large. A read-only CLI scan
        # must also return useful partial JSON when interrupted.
        scan.send_signal(signal.SIGINT)
        try:
            scan_out, scan_error = scan.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            scan.kill()
            scan.communicate()
            raise RuntimeError("native CLI scan did not cancel")
    if scan.returncode != 0:
        raise RuntimeError("native CLI failed: " + scan_error.decode(errors="replace"))
    snapshot = json.loads(scan_out)
    assert snapshot["Disk"]["Total"] > 0 and snapshot["Disk"]["Available"] > 0
    candidates = {item["Path"]: item for item in snapshot["Candidates"]}
    assert candidates[str(derived)]["Reclaimable"] and not candidates[str(derived)]["Protected"]
    assert candidates[str(support)]["Protected"], "missing device utilities must protect support folders"
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
    process = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
    os.close(slave)
    output = bytearray()
    sent_quit = False
    sent_cancel = False
    dashboard_seen_at = None
    deadline = time.monotonic() + 40
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(master, 65536)
                except OSError as exc:
                    if exc.errno != errno.EIO:
                        raise
                    break
                if not chunk:
                    break
                output.extend(chunk)
            # The isolated PATH makes all optional tools unavailable. Reaching
            # this state verifies that their failures leave a working TUI.
            if dashboard_seen_at is None and b"DISK OVERVIEW" in output:
                dashboard_seen_at = time.monotonic()
            if not sent_cancel and dashboard_seen_at is not None and time.monotonic() - dashboard_seen_at > 0.5:
                os.write(master, b"\x1b")
                sent_cancel = True
            if not sent_quit and (b"Scan finished with warnings" in output or b"Scan canceled; partial results" in output):
                os.write(master, b"q")
                sent_quit = True
            if process.poll() is not None:
                break
        if not sent_quit:
            raise RuntimeError("native TUI did not finish its isolated scan: " + output.decode(errors="replace")[-4000:])
        if process.wait(timeout=5) != 0:
            raise RuntimeError("native TUI exited unsuccessfully")
        if b"Developer Storage Cleaner" not in output or b"DISK OVERVIEW" not in output:
            raise RuntimeError("native TUI dashboard was not rendered")
        assert (derived / "fixture-cache").exists() and (support / "fixture-cache").exists()
        assert not (home / "Library/Application Support/devclean/history.json").exists()
        print("Native CLI + PTY smoke passed: real disk statistics, fixture discovery, device-service fail-closed protection, asynchronous scan/cancellation, Q exit, no startup deletion.")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        os.close(master)
