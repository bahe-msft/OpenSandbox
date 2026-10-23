"""Forward admitted origins to a fixed Gateway using the accepted private policy."""

import http.client as http_client
import ipaddress
import json
import os
import re
import socket
from pathlib import Path
from urllib.parse import urlsplit

from mitmproxy import ctx, http

GATEWAY_ENV = "OPENSANDBOX_EGRESS_TRANSPARENT_PROXY_GATEWAY"
TOKEN_ENV = "OPENSANDBOX_EGRESS_TRANSPARENT_PROXY_TOKEN_FILE"
SOCKET_ENV = "OPENSANDBOX_CREDENTIAL_PROXY_SOCKET"
DEFAULT_SOCKET = "/run/opensandbox/credential-proxy/active.sock"


def gateway():
    value = urlsplit(os.environ[GATEWAY_ENV])
    if (
        value.scheme != "http"
        or not value.hostname
        or not value.hostname.endswith(".svc.cluster.local")
        or value.port not in (None, 80)
        or value.username
        or value.password
        or value.path
        or value.query
        or value.fragment
    ):
        raise ValueError("invalid transparent Gateway")
    return value


def load(loader):
    try:
        gateway()
        if not Path(os.environ[TOKEN_ENV]).is_absolute():
            raise ValueError("invalid identity file")
        # No original server connection may occur before requestheaders chooses
        # the internal Gateway, including certificate discovery during TLS setup.
        ctx.options.update(upstream_cert=False)
    except Exception:
        raise SystemExit("transparent proxy: invalid configuration") from None


class PolicyConnection(http_client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(os.environ.get(SOCKET_ENV, DEFAULT_SOCKET))


def admitted_origins():
    connection = PolicyConnection("localhost", timeout=1)
    try:
        connection.request("GET", "/policy/_active")
        response = connection.getresponse()
        if response.status != 200:
            raise ValueError("policy unavailable")
        state = json.loads(response.read(64 * 1024))
        origins = state["origins"]
        if not isinstance(origins, list) or any(not isinstance(value, str) for value in origins):
            raise ValueError("invalid forwarding policy")
        return set(origins)
    finally:
        connection.close()


def origin_admitted(host, origins):
    if len(host) > 253 or not re.fullmatch(
        r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*", host
    ):
        return False
    try:
        ipaddress.ip_address(host)
    except ValueError:
        pass
    else:
        return False

    for origin in origins:
        if origin == "https://" + host:
            return True
        if origin.startswith("https://*.") and host.endswith(origin[len("https://*"):]):
            return True
    return False


def reject(flow, status):
    if flow.request.stream:
        flow.kill()
    else:
        flow.response = http.Response.make(status, b"transparent egress denied\n")


def requestheaders(flow):
    if flow.response is not None or flow.error is not None:
        return
    authority = flow.request.host_header or ""
    try:
        original = urlsplit("https://" + authority)
        host = original.hostname or ""
        origins = admitted_origins()
        admitted = origin_admitted(host, origins)
        if host.endswith(".svc.cluster.local") and not admitted:
            # Existing explicit provider endpoints retain system-addon bindings.
            return
        for name in list(flow.request.headers):
            lower = name.lower()
            if lower.startswith("x-turn-") or lower.startswith("x-aks-sandbox-"):
                del flow.request.headers[name]
            elif lower in ("authorization", "proxy-authorization", "api-key", "x-api-key", "cookie"):
                del flow.request.headers[name]
        if (
            flow.request.scheme != "https"
            or flow.request.port != 443
            or original.port not in (None, 443)
            or authority not in (host, host + ":443")
            or flow.client_conn.sni != host
            or not admitted
        ):
            reject(flow, 403)
            return

        destination = gateway()
        flow.request.scheme = "http"
        flow.request.host = destination.hostname
        flow.request.port = destination.port or 80
        flow.request.host_header = authority
        # The Gateway uses HTTP/1.1. Unknown-length HTTP/2 and HTTP/3 bodies
        # need chunk framing when mitmproxy converts the upstream request.
        if (flow.request.is_http2 or flow.request.is_http3) and "content-length" not in flow.request.headers:
            flow.request.headers["transfer-encoding"] = "chunked"
        token = Path(os.environ[TOKEN_ENV]).read_text().strip()
        if not token or "\r" in token or "\n" in token:
            raise ValueError("identity unavailable")
        flow.request.headers["x-aks-sandbox-identity"] = token
    except Exception:
        # No exception text or credential material crosses the proxy boundary.
        reject(flow, 503)
