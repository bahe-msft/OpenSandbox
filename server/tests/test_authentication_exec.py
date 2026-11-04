# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

import asyncio
import json
from pathlib import Path
import sys
from unittest.mock import AsyncMock, MagicMock

from fastapi import FastAPI, Request
from fastapi.testclient import TestClient
from pydantic import ValidationError
import pytest

from opensandbox_server import config as config_module
from opensandbox_server.config import AppConfig, RuntimeConfig, ServerConfig
from opensandbox_server.middleware.auth import AuthMiddleware
from opensandbox_server.services.authentication import (
    AUTHN_VERSION,
    AuthenticationDenied,
    AuthenticationResult,
    AuthenticationUnavailable,
    ExecAuthConfig,
    ExecAuthenticator,
)


def checker(tmp_path, code, timeout: float = 1):
    path = tmp_path / "checker.py"
    prelude = f"""import json
VERSION = {AUTHN_VERSION!r}
def output(allowed):
    print(json.dumps({{"version": VERSION, "allowed": allowed}}))
"""
    path.write_text(prelude + code)
    return ExecAuthenticator(
        ExecAuthConfig(command=[sys.executable, str(path)], timeout_seconds=timeout)
    )


@pytest.mark.asyncio
async def test_credentials_use_stdin_and_decision_is_consumed(tmp_path):
    auth = checker(
        tmp_path,
        """
import json, sys
assert len(sys.argv) == 1
assert json.load(sys.stdin) == {"version": VERSION, "token": "test-credential"}
output(True)
""",
    )
    result = await auth.authenticate("test-credential")
    assert result.allowed
    assert result.version == AUTHN_VERSION


@pytest.mark.asyncio
async def test_false_is_denied(tmp_path):
    with pytest.raises(AuthenticationDenied):
        await checker(tmp_path, "output(False)").authenticate("test-credential")


@pytest.mark.parametrize(
    "code",
    [
        'print("not JSON")',
        'output("true")',
        "output(1)",
        'print(json.dumps({"version": VERSION, "allowed": True, "subject": "unused"}))',
        'print(\'{"version":"v1","allowed":false,"allowed":true}\')',
        'print(\'{"version":"v2","version":"v1","allowed":true}\')',
        'print("x" * 100000)',
        "import sys; sys.exit(1)",
        "output(True); import sys; sys.exit(2)",
    ],
)
@pytest.mark.asyncio
async def test_bad_output_or_exit_fails_closed(tmp_path, code):
    with pytest.raises(AuthenticationUnavailable):
        await checker(tmp_path, code).authenticate("sensitive-test-value")


@pytest.mark.parametrize(
    "response",
    [
        {"allowed": True},
        {"version": "v2", "allowed": True},
        {"version": None, "allowed": True},
        {"version": 1, "allowed": True},
        {"version": True, "allowed": True},
        {"version": "v1"},
    ],
)
@pytest.mark.asyncio
async def test_missing_or_invalid_response_version_or_decision_fails_closed(tmp_path, response):
    with pytest.raises(AuthenticationUnavailable):
        await checker(tmp_path, f"print({json.dumps(response)!r})").authenticate("test-credential")


@pytest.mark.asyncio
async def test_timeout_and_stderr_do_not_leak_credentials(tmp_path, caplog):
    auth = checker(
        tmp_path,
        'import sys,time; print("sensitive-test-value",file=sys.stderr,flush=True);time.sleep(10)',
        timeout=0.1,
    )
    with pytest.raises(AuthenticationUnavailable) as error:
        await asyncio.wait_for(auth.authenticate("sensitive-test-value"), timeout=2)
    assert "sensitive-test-value" not in str(error.value) + caplog.text


@pytest.mark.asyncio
async def test_missing_executable_fails_closed():
    auth = ExecAuthenticator(ExecAuthConfig(command=["/does/not/exist"]))
    with pytest.raises(AuthenticationUnavailable):
        await auth.authenticate("test-credential")


@pytest.mark.asyncio
async def test_queue_wait_counts_towards_deadline(tmp_path, monkeypatch):
    auth = checker(tmp_path, "output(True)", timeout=0.01)
    spawn = AsyncMock()
    monkeypatch.setattr(asyncio, "create_subprocess_exec", spawn)
    for _ in range(auth.MAX_CONCURRENCY):
        await auth._slots.acquire()
    with pytest.raises(AuthenticationUnavailable):
        await auth.authenticate("test-credential")
    spawn.assert_not_called()


def test_configuration_file_owns_timeout_and_provider_settings(tmp_path):
    path = tmp_path / "authn.toml"
    path.write_text(
        f'[exec]\ncommand=[{json.dumps(sys.executable)}, "checker.py"]\ntimeout_seconds=2\n[provider]\nissuer="outside-osb"\n'
    )
    loaded = ExecAuthConfig.load(path)
    assert loaded.timeout_seconds == 2
    assert loaded.command[0] == sys.executable


@pytest.mark.parametrize(
    "extra",
    [
        {"command": ["relative-path"]},
        {"command": []},
        {"timeout_seconds": 0},
        {"timeout_seconds": 31},
        {"command": [sys.executable, "\x00bad"]},
        {"unknown": True},
    ],
)
def test_bad_exec_configuration_rejected(extra):
    with pytest.raises(ValidationError):
        ExecAuthConfig.model_validate({"command": [sys.executable], **extra})


@pytest.mark.parametrize("api_key", ["existing-key", ""])
def test_only_one_server_authentication_option(api_key):
    with pytest.raises(ValidationError, match="Only one"):
        AppConfig.model_validate(
            {
                "runtime": {"type": "docker", "execd_image": "execd:test"},
                "server": {"api_key": api_key, "authn_config": "/etc/osb/authn.toml"},
            }
        )


def test_environment_override_cannot_enable_two_modes(tmp_path, monkeypatch):
    path = tmp_path / "server.toml"
    path.write_text(
        '[runtime]\ntype="docker"\nexecd_image="execd:test"\n[server]\nauthn_config="/etc/osb/authn.toml"\n'
    )
    monkeypatch.setattr(config_module, "_config", None)
    monkeypatch.setattr(config_module, "_config_path", None)
    monkeypatch.setenv("OPENSANDBOX_SERVER_API_KEY", "existing-key")
    with pytest.raises(ValidationError, match="Only one"):
        config_module.load_config(path)


@pytest.mark.parametrize("runtime", ["docker", "kubernetes"])
def test_existing_key_and_exec_modes_remain_available(runtime):
    for server in ({"api_key": "existing-key"}, {"authn_config": "/etc/osb/authn.toml"}):
        AppConfig.model_validate(
            {"runtime": {"type": runtime, "execd_image": "execd:test"}, "server": server}
        )


def test_relative_config_path_rejected():
    with pytest.raises(ValidationError):
        ServerConfig(authn_config=Path("relative.toml"))


@pytest.fixture
def app_and_authenticator():
    app = FastAPI()
    auth = MagicMock()
    auth.authenticate = AsyncMock(
        return_value=AuthenticationResult(version=AUTHN_VERSION, allowed=True)
    )
    app.state.authenticator = auth
    config = AppConfig(
        runtime=RuntimeConfig(type="docker", execd_image="execd:test"),
        server=ServerConfig(authn_config=Path("/etc/osb/authn.toml")),
    )
    app.add_middleware(AuthMiddleware, config=config)

    @app.get("/health")
    @app.get("/sandboxes/s1/proxy/80/")
    @app.get("/v1/sandboxes/s1/proxy/80/")
    def public_or_proxy():
        return {"ok": True}

    @app.get("/secured")
    @app.get("/v1/secured")
    @app.get("/health/private")
    def secured(request: Request):
        assert not hasattr(request.state, "auth_subject")
        return {"ok": True}

    return app, auth


def test_management_calls_use_checker(app_and_authenticator):
    app, auth = app_and_authenticator
    for path in ("/secured", "/v1/secured", "/health/private"):
        response = TestClient(app).get(path, headers={"Authorization": "Bearer test-credential"})
        assert response.status_code == 200 and response.json() == {"ok": True}
    assert auth.authenticate.await_count == 3


@pytest.mark.parametrize(
    "headers",
    [
        {},
        {"OPEN-SANDBOX-API-KEY": "old-key"},
        {"Authorization": "Bearer"},
        [("Authorization", "Bearer a"), ("Authorization", "Bearer b")],
    ],
)
def test_missing_credentials_never_fall_back(app_and_authenticator, headers):
    app, auth = app_and_authenticator
    response = TestClient(app).get("/secured", headers=headers)
    assert response.status_code == 401 and response.headers["WWW-Authenticate"] == "Bearer"
    auth.authenticate.assert_not_awaited()


@pytest.mark.parametrize(
    "error,status", [(AuthenticationDenied(), 401), (AuthenticationUnavailable(), 503)]
)
def test_checker_failure_rejects_management_request(app_and_authenticator, error, status):
    app, auth = app_and_authenticator
    auth.authenticate.side_effect = error
    response = TestClient(app).get("/secured", headers={"Authorization": "Bearer test-credential"})
    assert response.status_code == status


def test_data_plane_and_public_paths_unchanged(app_and_authenticator):
    app, auth = app_and_authenticator
    for path in ("/health", "/sandboxes/s1/proxy/80/", "/v1/sandboxes/s1/proxy/80/"):
        assert TestClient(app).get(path).status_code == 200
    auth.authenticate.assert_not_awaited()
