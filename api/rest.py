"""
REST API and WebSocket endpoints for GLM control.

Provides HTTP endpoints for control and WebSocket for real-time state updates.
"""
import asyncio
import logging
import os
import threading
import time
from pathlib import Path
from typing import Optional, Set
from contextlib import asynccontextmanager

from fastapi import FastAPI, WebSocket, WebSocketDisconnect
from fastapi.responses import JSONResponse, FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

from glm_core import SetVolume, AdjustVolume, SetMute, SetDim, SetPower, QueuedAction

logger = logging.getLogger(__name__)

# Will be set by create_app()
_action_queue = None
_glm_controller = None

# Track connected WebSocket clients
_websocket_clients: Set[WebSocket] = set()
_ws_lock = threading.Lock()

# Event loop for the API server thread (set when server starts)
_api_event_loop = None


# Pydantic models for request validation
class VolumeRequest(BaseModel):
    value: int  # 0-127


class VolumeAdjustRequest(BaseModel):
    delta: int  # positive or negative


class StateRequest(BaseModel):
    state: Optional[bool] = None  # None = toggle


class PowerRequest(BaseModel):
    state: Optional[bool] = None  # None = toggle, True = ON, False = OFF


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Manage startup and shutdown."""
    # Register state callback for WebSocket broadcast
    _glm_controller.add_state_callback(_broadcast_state_sync)
    logger.info("API server started, WebSocket broadcast registered")
    yield
    # Cleanup
    _glm_controller.remove_state_callback(_broadcast_state_sync)
    logger.info("API server stopped")


def create_app(action_queue, glm_controller) -> FastAPI:
    """
    Create FastAPI app with references to the action queue and controller.

    Args:
        action_queue: The queue.Queue for submitting GlmActions
        glm_controller: The GlmController instance for reading state

    Returns:
        Configured FastAPI app
    """
    global _action_queue, _glm_controller
    _action_queue = action_queue
    _glm_controller = glm_controller

    app = FastAPI(
        title="GLM Control API",
        description="REST API for Genelec GLM speaker control",
        version="1.0.0",
        lifespan=lifespan
    )

    # Register API routes
    app.get("/api/state")(get_state)
    app.post("/api/volume")(set_volume)
    app.post("/api/volume/adjust")(adjust_volume)
    app.post("/api/mute")(set_mute)
    app.post("/api/dim")(set_dim)
    app.post("/api/power")(set_power)
    app.get("/api/health")(health_check)
    app.websocket("/ws/state")(websocket_state)

    # Serve web UI
    @app.get("/")
    async def serve_index():
        """Serve the web UI."""
        web_dir = Path(__file__).parent.parent / "web"
        index_path = web_dir / "index.html"
        if index_path.exists():
            return FileResponse(index_path, media_type="text/html")
        return JSONResponse({"error": "Web UI not found"}, status_code=404)

    return app


def _submit_action(action):
    """Submit an action to the queue."""
    if _action_queue is None:
        logger.error("Action queue not initialized")
        return False, "not_initialized"
    _action_queue.put(QueuedAction(action=action, timestamp=time.time()))
    return True, None


def _check_settling():
    """Check if system is settling (power transition in progress)."""
    if _glm_controller is None:
        return False, 0
    allowed, wait_time, reason = _glm_controller.can_accept_command()
    return not allowed, wait_time


def _check_power_cooldown():
    """Check if power command is in cooldown."""
    if _glm_controller is None:
        return False, 0, None
    allowed, wait_time, reason = _glm_controller.can_accept_power_command()
    return not allowed, wait_time, reason


def _broadcast_state_sync(state: dict):
    """
    Synchronous callback for state changes - schedules async broadcast.
    Called from GlmController in various threads.
    """
    global _api_event_loop

    if _api_event_loop is None:
        logger.debug("API event loop not ready, skipping broadcast")
        return

    with _ws_lock:
        clients = list(_websocket_clients)

    if not clients:
        return

    # Schedule broadcast in the API server's event loop
    try:
        asyncio.run_coroutine_threadsafe(_broadcast_to_all(clients, state), _api_event_loop)
    except Exception as e:
        logger.debug(f"Failed to schedule WebSocket broadcast: {e}")


async def _broadcast_to_all(clients: list, state: dict):
    """Broadcast state to all WebSocket clients."""
    for ws in clients:
        await _send_state_to_client(ws, state)


async def _send_state_to_client(ws: WebSocket, state: dict):
    """Send state to a single WebSocket client."""
    try:
        await ws.send_json(state)
    except Exception as e:
        logger.debug(f"Failed to send to WebSocket client: {e}")
        with _ws_lock:
            _websocket_clients.discard(ws)


# === REST Endpoints ===

async def get_state():
    """Get current GLM state."""
    if _glm_controller is None:
        return JSONResponse({"error": "Controller not initialized"}, status_code=503)
    return _glm_controller.get_state()


async def set_volume(request: VolumeRequest):
    """Set absolute volume (0-127)."""
    # Check if settling
    settling, wait_time = _check_settling()
    if settling:
        return JSONResponse(
            {"error": "Power settling in progress", "retry_after": round(wait_time, 1)},
            status_code=503,
            headers={"Retry-After": str(int(wait_time) + 1)}
        )

    value = max(0, min(127, request.value))
    success, err = _submit_action(SetVolume(target=value))
    if success:
        return {"status": "ok", "action": "set_volume", "value": value}
    return JSONResponse({"error": "Failed to submit action"}, status_code=500)


async def adjust_volume(request: VolumeAdjustRequest):
    """Adjust volume by delta (positive = up, negative = down)."""
    # Check if settling
    settling, wait_time = _check_settling()
    if settling:
        return JSONResponse(
            {"error": "Power settling in progress", "retry_after": round(wait_time, 1)},
            status_code=503,
            headers={"Retry-After": str(int(wait_time) + 1)}
        )

    success, err = _submit_action(AdjustVolume(delta=request.delta))
    if success:
        return {"status": "ok", "action": "adjust_volume", "delta": request.delta}
    return JSONResponse({"error": "Failed to submit action"}, status_code=500)


async def set_mute(request: StateRequest = StateRequest()):
    """Set or toggle mute. Send {"state": true/false} or {} for toggle."""
    # Check if settling
    settling, wait_time = _check_settling()
    if settling:
        return JSONResponse(
            {"error": "Power settling in progress", "retry_after": round(wait_time, 1)},
            status_code=503,
            headers={"Retry-After": str(int(wait_time) + 1)}
        )

    success, err = _submit_action(SetMute(state=request.state))
    if success:
        action_desc = f"set to {request.state}" if request.state is not None else "toggle"
        return {"status": "ok", "action": "mute", "mode": action_desc}
    return JSONResponse({"error": "Failed to submit action"}, status_code=500)


async def set_dim(request: StateRequest = StateRequest()):
    """Set or toggle dim. Send {"state": true/false} or {} for toggle."""
    # Check if settling
    settling, wait_time = _check_settling()
    if settling:
        return JSONResponse(
            {"error": "Power settling in progress", "retry_after": round(wait_time, 1)},
            status_code=503,
            headers={"Retry-After": str(int(wait_time) + 1)}
        )

    success, err = _submit_action(SetDim(state=request.state))
    if success:
        action_desc = f"set to {request.state}" if request.state is not None else "toggle"
        return {"status": "ok", "action": "dim", "mode": action_desc}
    return JSONResponse({"error": "Failed to submit action"}, status_code=500)


async def set_power(request: PowerRequest = PowerRequest()):
    """
    Set or toggle power.

    Send {} for toggle, {"state": true} for ON, {"state": false} for OFF.
    """
    # Check power cooldown (longer than settling)
    blocked, wait_time, reason = _check_power_cooldown()
    if blocked:
        if reason == "power_settling":
            msg = "Power settling in progress"
        else:
            msg = "Power cooldown active"
        return JSONResponse(
            {"error": msg, "retry_after": round(wait_time, 1)},
            status_code=503,
            headers={"Retry-After": str(int(wait_time) + 1)}
        )

    success, err = _submit_action(SetPower(state=request.state))
    if success:
        if request.state is None:
            mode = "toggle"
        else:
            mode = "on" if request.state else "off"
        return {"status": "ok", "action": "power", "mode": mode}
    return JSONResponse({"error": "Failed to submit action"}, status_code=500)


async def health_check():
    """Health check endpoint."""
    return {
        "status": "ok",
        "volume_initialized": _glm_controller.has_valid_volume if _glm_controller else False,
    }


# === WebSocket Endpoint ===

async def websocket_state(websocket: WebSocket):
    """WebSocket endpoint for real-time state updates."""
    await websocket.accept()

    with _ws_lock:
        _websocket_clients.add(websocket)
    logger.info(f"WebSocket client connected. Total: {len(_websocket_clients)}")

    # Send current state immediately
    if _glm_controller:
        await websocket.send_json(_glm_controller.get_state())

    try:
        # Keep connection alive, ignore incoming messages
        while True:
            await websocket.receive_text()
    except WebSocketDisconnect:
        pass
    except (OSError, asyncio.CancelledError, ConnectionResetError, Exception) as e:
        # Windows semaphore timeout, connection reset, or other connection errors
        # These are expected when client disconnects abruptly (close tab, network drop)
        logger.debug(f"WebSocket connection closed: {type(e).__name__}")
    finally:
        with _ws_lock:
            _websocket_clients.discard(websocket)
        logger.info(f"WebSocket client disconnected. Total: {len(_websocket_clients)}")


def start_api_server(action_queue, glm_controller, host: str = "0.0.0.0", port: int = 8080):
    """
    Start the API server in a background thread.

    Args:
        action_queue: The queue.Queue for submitting GlmActions
        glm_controller: The GlmController instance
        host: Bind address (default: 0.0.0.0)
        port: Port number (default: 8080)

    Returns:
        The server thread
    """
    import uvicorn
    global _api_event_loop

    # Suppress verbose error logging for expected WebSocket disconnects
    # (Windows semaphore timeout, connection reset, etc.)
    logging.getLogger("websockets.legacy.protocol").setLevel(logging.CRITICAL)
    logging.getLogger("websockets.protocol").setLevel(logging.CRITICAL)
    logging.getLogger("websockets.legacy.server").setLevel(logging.CRITICAL)
    # Suppress uvicorn's error logger which also catches and prints these exceptions
    logging.getLogger("uvicorn.error").setLevel(logging.CRITICAL)

    app = create_app(action_queue, glm_controller)

    config = uvicorn.Config(
        app,
        host=host,
        port=port,
        log_level="warning",  # Reduce uvicorn noise
        access_log=False,
    )
    server = uvicorn.Server(config)

    def run_server():
        global _api_event_loop
        # Create new event loop for this thread
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        _api_event_loop = loop  # Store for cross-thread WebSocket broadcasts
        loop.run_until_complete(server.serve())

    thread = threading.Thread(target=run_server, name="APIServerThread", daemon=True)
    thread.start()
    logger.info(f"API server starting on http://{host}:{port}")

    return thread
