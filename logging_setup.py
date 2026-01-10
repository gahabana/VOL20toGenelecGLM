"""
Logging Setup for GLM Manager.

Provides centralized logging configuration with:
- Rotating file handler
- Console handler
- Async queue-based logging for thread safety
- WebSocket error filtering
"""

import logging
import os
import threading
from logging.handlers import RotatingFileHandler, QueueHandler, QueueListener
from queue import Queue
from typing import Callable, Optional

# Centralized logging format with thread, module, function, and line number
LOG_FORMAT = '%(asctime)s [%(levelname)s] %(threadName)s %(module)s:%(funcName)s:%(lineno)d - %(message)s'


def setup_logging(
    log_level: str,
    log_file_name: str,
    script_dir: str,
    version: str = "",
    script_name: str = "GLM Manager",
    max_bytes: int = 4*1024*1024,
    backup_count: int = 5,
    set_thread_priority_func: Optional[Callable] = None,
    thread_priority_idle: int = 0
) -> tuple:
    """
    Set up logging with rotating file handler and console output.

    Args:
        log_level: Logging level ("DEBUG", "INFO", or "NONE")
        log_file_name: Name of the log file
        script_dir: Directory where log file should be created
        version: Version string to log at startup
        script_name: Name of the script for startup message
        max_bytes: Maximum size of log file before rotation
        backup_count: Number of backup log files to keep
        set_thread_priority_func: Optional function to set thread priority
        thread_priority_idle: Priority level for logging thread

    Returns:
        Tuple of (logger, stop_logging_func)
    """
    log_file_path = os.path.join(script_dir, log_file_name)

    log_queue = Queue()

    # Import WebSocket error filter to suppress disconnect errors in logs
    try:
        from api.rest import WebSocketErrorFilter
        ws_filter = WebSocketErrorFilter()
    except ImportError:
        ws_filter = None

    # File Handler
    file_handler = RotatingFileHandler(log_file_path, maxBytes=max_bytes, backupCount=backup_count)
    file_handler.setLevel(logging.DEBUG if log_level != "NONE" else logging.CRITICAL)
    file_handler.setFormatter(logging.Formatter(LOG_FORMAT))
    if ws_filter:
        file_handler.addFilter(ws_filter)

    # Console Handler
    console_handler = logging.StreamHandler()
    console_handler.setLevel(logging.INFO if log_level in ["INFO", "DEBUG"] else logging.CRITICAL)
    console_handler.setFormatter(logging.Formatter(LOG_FORMAT))
    if ws_filter:
        console_handler.addFilter(ws_filter)

    # QueueHandler
    queue_handler = QueueHandler(log_queue)

    # Root Logger
    root_logger = logging.getLogger()
    root_logger.handlers = []  # Clear all handlers
    root_logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    root_logger.addHandler(queue_handler)

    # Module Logger
    logger = logging.getLogger(__name__)
    logger.setLevel(logging.DEBUG if log_level == "DEBUG" else logging.INFO)
    logger.addHandler(console_handler)
    logger.addHandler(file_handler)
    logger.propagate = False  # Avoid double logging

    # Listener Thread
    stop_event = threading.Event()

    # Log startup message
    version_str = f" v{version}" if version else ""
    logger.info(f">----- Starting {script_name}{version_str}. Initializing...")

    def log_listener_thread():
        listener = QueueListener(log_queue, file_handler, console_handler)
        listener.start()

        # Lower thread priority if function provided
        if set_thread_priority_func:
            set_thread_priority_func(thread_priority_idle)

        stop_event.wait()
        listener.stop()

    logging_thread = threading.Thread(target=log_listener_thread, name="LoggingThread", daemon=False)
    logging_thread.start()

    def stop_logging():
        stop_event.set()

    return logger, stop_logging
