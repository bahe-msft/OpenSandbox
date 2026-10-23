"""Tests for the production forwarding addon; no mitmproxy installation needed."""

import importlib.util
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch


class TransparentProxyTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.token = Path(directory.name) / "token"
        self.token.write_text("identity-one\n")
        environment = patch.dict(os.environ, {
            "OPENSANDBOX_EGRESS_TRANSPARENT_PROXY_GATEWAY": "http://gateway.system.svc.cluster.local",
            "OPENSANDBOX_EGRESS_TRANSPARENT_PROXY_TOKEN_FILE": str(self.token),
        })
        environment.start()
        self.addCleanup(environment.stop)
        response = types.SimpleNamespace(make=lambda status, body: types.SimpleNamespace(status_code=status, body=body))
        modules = patch.dict(sys.modules, {"mitmproxy": types.SimpleNamespace(ctx=None, http=types.SimpleNamespace(Response=response))})
        modules.start()
        self.addCleanup(modules.stop)
        path = Path(__file__).parents[1] / "mitmscripts" / "transparent_proxy.py"
        spec = importlib.util.spec_from_file_location("transparent_proxy", path)
        self.addon = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.addon)
        self.origins = {"https://github.com"}
        self.addon.admitted_origins = lambda: self.origins
        self.flow = self.new_flow()

    def new_flow(self):
        value = types.SimpleNamespace(
            request=types.SimpleNamespace(
                host="github.com", host_header="github.com", scheme="https", port=443,
                path="/owner/repo.git/git-upload-pack", stream=False, content=b"git body",
                is_http2=False, is_http3=False,
                headers={"x-aks-sandbox-identity": "forged", "authorization": "caller"},
            ),
            client_conn=types.SimpleNamespace(sni="github.com"), response=None, error=None,
        )
        value.kill = lambda: setattr(value, "error", "killed")
        return value

    def test_forward_uses_fixed_gateway_and_preserves_request(self):
        self.addon.requestheaders(self.flow)
        self.assertIsNone(self.flow.response)
        self.assertEqual(self.flow.request.host, "gateway.system.svc.cluster.local")
        self.assertEqual(self.flow.request.host_header, "github.com")
        self.assertEqual(self.flow.request.scheme, "http")
        self.assertEqual(self.flow.request.port, 80)
        self.assertEqual(self.flow.request.path, "/owner/repo.git/git-upload-pack")
        self.assertEqual(self.flow.request.content, b"git body")
        self.assertEqual(self.flow.request.headers, {"x-aks-sandbox-identity": "identity-one"})

    def test_unknown_length_modern_http_body_gets_chunk_framing(self):
        for protocol in ("is_http2", "is_http3"):
            with self.subTest(protocol=protocol):
                flow = self.new_flow()
                setattr(flow.request, protocol, True)
                self.addon.requestheaders(flow)
                self.assertIsNone(flow.response)
                self.assertEqual(flow.request.headers["transfer-encoding"], "chunked")
                self.assertEqual(flow.request.content, b"git body")

    def test_known_length_http2_body_keeps_content_length(self):
        self.flow.request.is_http2 = True
        self.flow.request.headers["content-length"] = "8"
        self.addon.requestheaders(self.flow)
        self.assertIsNone(self.flow.response)
        self.assertNotIn("transfer-encoding", self.flow.request.headers)
        self.assertEqual(self.flow.request.headers["content-length"], "8")

    def test_wildcard_preserves_concrete_authority_and_observes_removal(self):
        self.origins = {"https://*.kusto.windows.net"}
        host = "cluster.westus2.kusto.windows.net"
        self.flow.request.host = host
        self.flow.request.host_header = host + ":443"
        self.flow.client_conn.sni = host
        self.addon.requestheaders(self.flow)
        self.assertIsNone(self.flow.response)
        self.assertEqual(self.flow.request.host_header, host + ":443")
        self.assertEqual(self.flow.request.host, "gateway.system.svc.cluster.local")

        self.origins.clear()
        flow = self.new_flow()
        flow.request.host_header = host
        flow.client_conn.sni = host
        self.addon.requestheaders(flow)
        self.assertEqual(flow.response.status_code, 403)
        self.assertNotIn("x-aks-sandbox-identity", flow.request.headers)

    def test_wildcard_rejects_apex_suffix_spoof_and_nonconcrete_names(self):
        origins = {"https://*.kusto.windows.net"}
        for host in (
            "kusto.windows.net", "notkusto.windows.net",
            "cluster.kusto.windows.net.evil.example", "*.kusto.windows.net",
            "cluster..kusto.windows.net", "cluster.kusto.windows.net/evil",
        ):
            with self.subTest(host=host):
                self.assertFalse(self.addon.origin_admitted(host, origins))
        self.assertFalse(self.addon.origin_admitted("1.2.3.4", {"https://*.2.3.4"}))

    def test_origin_removal_is_seen_without_restart(self):
        self.origins.clear()
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.response.status_code, 403)
        self.assertNotIn("x-aks-sandbox-identity", self.flow.request.headers)

    def test_rotating_token_is_not_cached(self):
        self.token.write_text("identity-two\n")
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.request.headers["x-aks-sandbox-identity"], "identity-two")

    def test_host_and_sni_must_match(self):
        self.flow.client_conn.sni = "dev.azure.com"
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.response.status_code, 403)

    def test_unreadable_identity_never_falls_back(self):
        self.token.unlink()
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.response.status_code, 503)
        self.assertEqual(self.flow.request.host, "gateway.system.svc.cluster.local")
        self.assertNotIn("x-aks-sandbox-identity", self.flow.request.headers)

    def test_streamed_denial_kills_request(self):
        self.flow.request.stream = True
        self.origins.clear()
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.error, "killed")

    def test_private_policy_failure_denies_request(self):
        def unavailable():
            raise OSError("unavailable")
        self.addon.admitted_origins = unavailable
        self.addon.requestheaders(self.flow)
        self.assertEqual(self.flow.response.status_code, 503)

    def test_explicit_internal_provider_is_unchanged(self):
        self.flow.request.host_header = "provider.system.svc.cluster.local"
        self.flow.request.headers["x-aks-sandbox-identity"] = "system-injected"
        self.flow.request.scheme = "http"
        self.flow.request.port = 80
        self.addon.requestheaders(self.flow)
        self.assertIsNone(self.flow.response)
        self.assertEqual(self.flow.request.headers["x-aks-sandbox-identity"], "system-injected")


if __name__ == "__main__":
    unittest.main()
