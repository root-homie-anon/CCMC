"""
CCMC iTerm2 status bar component.

Connects to the CCMC daemon via its unix socket, calls GET /status every 5
seconds, and renders a compact session summary in the iTerm2 status bar.

Display format:
  CC: N active · M idle   (green when active, yellow when only idle, red on error)
  CC: —                   (red when daemon is unreachable)

Installation:
  Place in ~/Library/ApplicationSupport/iTerm2/Scripts/AutoLaunch/ (or any
  AutoLaunch subdirectory) and enable in iTerm2 → Scripts menu.

Requirements:
  - Python 3.10+
  - iterm2 package (ships with iTerm2's Python runtime; do not install separately)
  - CCMC daemon running (ccmc daemon start)
"""

from __future__ import annotations

import json
import os
import socket
from typing import Optional

import iterm2


# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# Default socket path mirrors internal/config/paths.go: CcmcDir()/ccmc.sock.
# CcmcDir() defaults to ~/.ccmc; respects CCMC_DIR env var.
def _socket_path() -> str:
    ccmc_dir = os.environ.get("CCMC_DIR") or os.path.expanduser("~/.ccmc")
    return os.path.join(ccmc_dir, "ccmc.sock")


POLL_INTERVAL_SECONDS: float = 5.0

# Component metadata shown in the iTerm2 status bar configuration UI.
_COMPONENT_ID = "com.ccmc.statusbar"
_KNOB_SOCKET = "socket_path"


# ---------------------------------------------------------------------------
# HTTP-over-unix-socket client
# ---------------------------------------------------------------------------

def _http_get_unix(sock_path: str, path: str, timeout: float = 3.0) -> bytes:
    """
    Send a minimal HTTP/1.0 GET request over a unix domain socket and return
    the raw response body bytes.

    Raises socket.error, ConnectionRefusedError, FileNotFoundError, or
    OSError on any connectivity failure — callers catch the broad socket.error
    base which covers all of these.
    """
    request = (
        f"GET {path} HTTP/1.0\r\n"
        f"Host: localhost\r\n"
        f"Connection: close\r\n"
        f"\r\n"
    ).encode()

    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(timeout)
        sock.connect(sock_path)
        sock.sendall(request)

        chunks: list[bytes] = []
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            chunks.append(chunk)

    raw = b"".join(chunks)

    # Split HTTP response: headers\r\n\r\nbody
    separator = b"\r\n\r\n"
    idx = raw.find(separator)
    if idx == -1:
        raise ValueError("malformed HTTP response: no header/body separator")

    return raw[idx + len(separator):]


# ---------------------------------------------------------------------------
# Status polling
# ---------------------------------------------------------------------------

class DaemonStatus:
    """Parsed subset of the DaemonStatus JSON from GET /status."""

    __slots__ = ("running", "active_count", "idle_count", "session_count")

    def __init__(
        self,
        running: bool,
        active_count: int,
        idle_count: int,
        session_count: int,
    ) -> None:
        self.running = running
        self.active_count = active_count
        self.idle_count = idle_count
        self.session_count = session_count


def _fetch_status(sock_path: str) -> Optional[DaemonStatus]:
    """
    Query GET /status on the unix socket. Returns None when the daemon is
    unreachable or returns an unexpected response shape.
    """
    try:
        body = _http_get_unix(sock_path, "/status")
    except (socket.error, OSError, ValueError):
        return None

    try:
        data: dict = json.loads(body)
    except json.JSONDecodeError:
        return None

    # Validate minimum required fields; treat missing fields as daemon error.
    if not isinstance(data.get("running"), bool):
        return None

    active: int = int(data.get("activeCount", 0))
    # idleCount = sessionCount - activeCount when idleCount field is absent
    # (older daemon builds may omit it). Prefer the explicit field.
    idle: int = int(data.get("idleCount", data.get("sessionCount", 0) - active))
    session: int = int(data.get("sessionCount", active + idle))

    return DaemonStatus(
        running=bool(data["running"]),
        active_count=active,
        idle_count=max(idle, 0),
        session_count=session,
    )


# ---------------------------------------------------------------------------
# Rendering helpers
# ---------------------------------------------------------------------------

def _render(status: Optional[DaemonStatus]) -> tuple[str, iterm2.StatusBarComponent.Color]:
    """
    Return (label_text, color) for the given daemon status.

    Color mapping:
      Green  — daemon healthy and at least one active session
      Yellow — daemon healthy but no active sessions (all idle or empty)
      Red    — daemon unreachable, returned error, or running=False
    """
    Color = iterm2.StatusBarComponent.Color

    if status is None or not status.running:
        return "CC: —", Color.RED

    if status.active_count > 0:
        label = f"CC: {status.active_count} active · {status.idle_count} idle"
        return label, Color.GREEN

    if status.session_count > 0:
        label = f"CC: 0 active · {status.idle_count} idle"
        return label, Color.YELLOW

    # Daemon is running but registry is empty.
    return "CC: 0 sessions", Color.YELLOW


# ---------------------------------------------------------------------------
# iTerm2 entry point
# ---------------------------------------------------------------------------

async def main(connection: iterm2.Connection) -> None:
    knobs = [
        iterm2.StringKnob(
            name="Socket path",
            placeholder=_socket_path(),
            default_value="",
            key=_KNOB_SOCKET,
        )
    ]

    component = iterm2.StatusBarComponent(
        short_description="CCMC Sessions",
        detailed_description="Shows active and idle Claude Code sessions via CCMC daemon",
        knobs=knobs,
        exemplar="CC: 2 active · 1 idle",
        update_cadence=POLL_INTERVAL_SECONDS,
        identifier=_COMPONENT_ID,
    )

    @iterm2.StatusBarRPC
    async def coro(knobs: dict) -> str:
        sock_path = knobs.get(_KNOB_SOCKET) or _socket_path()
        status = _fetch_status(sock_path)
        label, color = _render(status)

        # iterm2.StatusBarComponent supports returning a list of
        # [label, color_hint] to set the text color. Use the string-only path
        # for maximum compatibility; color metadata is attached via the
        # StatusBarComponent color attribute where the API supports it.
        # As of iTerm2 3.4+ the RPC can return a styled string via
        # iterm2.StatusBarComponent.V2 — fall back gracefully if unavailable.
        try:
            styled = iterm2.StatusBarComponent.StyledString(label, color=color)
            return styled  # type: ignore[return-value]
        except AttributeError:
            return label

    await component.async_register(connection, coro)


iterm2.run_forever(main)
