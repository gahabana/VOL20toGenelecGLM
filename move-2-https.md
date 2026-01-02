# Moving to HTTPS

This document explains how to enable HTTPS for the GLM Control web interface.

## Current State

The frontend already supports both HTTP and HTTPS - the WebSocket URL automatically adapts based on how the page is accessed.

## Option 1: Self-Signed Certificate (LAN)

Best for local network use. Browser will show a warning once.

### Generate Certificate

```bash
# Run from project directory
openssl req -x509 -newkey rsa:4096 \
    -keyout ssl/key.pem \
    -out ssl/cert.pem \
    -days 365 -nodes \
    -subj '/CN=glm-control'
```

Create the `ssl/` directory first: `mkdir -p ssl`

### Backend Code Changes

**In `api/rest.py`**, modify `start_api_server()`:

```python
def start_api_server(action_queue, glm_controller, host: str = "0.0.0.0", port: int = 8080,
                     ssl_keyfile: str = None, ssl_certfile: str = None):
    # ... existing code ...

    config = uvicorn.Config(
        app,
        host=host,
        port=port,
        log_level="warning",
        access_log=False,
        ssl_keyfile=ssl_keyfile,      # Add this
        ssl_certfile=ssl_certfile,    # Add this
    )
```

**In `glm_server.py`**, add argument parsing:

```python
import argparse

parser = argparse.ArgumentParser()
parser.add_argument('--ssl-cert', help='Path to SSL certificate file')
parser.add_argument('--ssl-key', help='Path to SSL key file')
parser.add_argument('--port', type=int, default=8080)
args = parser.parse_args()

# When starting server:
start_api_server(
    action_queue,
    glm_controller,
    port=args.port,
    ssl_keyfile=args.ssl_key,
    ssl_certfile=args.ssl_cert
)
```

### Running with HTTPS

```bash
python glm_server.py --ssl-cert ssl/cert.pem --ssl-key ssl/key.pem --port 8443
```

Access at `https://your-ip:8443`

## Option 2: Tailscale HTTPS (Recommended for Tailscale Users)

Provides valid, trusted certificates with no browser warnings.

### Get Certificate

```bash
# Get your machine's Tailscale hostname
tailscale status

# Generate cert (requires Tailscale 1.14+)
tailscale cert your-machine.your-tailnet.ts.net
```

This creates:
- `your-machine.your-tailnet.ts.net.crt`
- `your-machine.your-tailnet.ts.net.key`

### Run Server

```bash
python glm_server.py \
    --ssl-cert your-machine.your-tailnet.ts.net.crt \
    --ssl-key your-machine.your-tailnet.ts.net.key \
    --port 443
```

Access at `https://your-machine.your-tailnet.ts.net`

Note: Port 443 may require root/admin privileges. Use 8443 if needed.

## Option 3: Reverse Proxy (Caddy)

If you prefer not to modify the Python code, use Caddy as a reverse proxy.

### Install Caddy

```bash
# macOS
brew install caddy

# Linux
sudo apt install caddy
```

### Caddyfile (for LAN with self-signed)

```
{
    auto_https disable_redirects
}

:8443 {
    tls internal
    reverse_proxy localhost:8080
}
```

### Run

```bash
# Start GLM server on HTTP (default)
python glm_server.py

# Start Caddy (separate terminal)
caddy run
```

Access at `https://your-ip:8443`

## Security Notes

- Self-signed certs encrypt traffic but don't verify server identity
- First connection will show browser warning - this is expected
- You can add the cert to your OS/browser trust store to remove warnings
- Tailscale certs are fully trusted (no warnings)

## Files to Add to .gitignore

```
ssl/
*.pem
*.crt
*.key
```
