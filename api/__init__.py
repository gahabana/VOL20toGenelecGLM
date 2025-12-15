"""REST API and WebSocket for GLM control."""
from .rest import create_app, start_api_server

__all__ = ['create_app', 'start_api_server']
