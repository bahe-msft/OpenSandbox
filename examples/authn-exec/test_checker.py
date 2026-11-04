# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

import io
import json
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

import checker


@pytest.fixture
def reviewer(monkeypatch):
    api = MagicMock()
    api.create_token_review.return_value = SimpleNamespace(
        status=SimpleNamespace(
            authenticated=True,
            audiences=["opensandbox"],
            user=SimpleNamespace(username="system:serviceaccount:osb-authn:client"),
        )
    )
    monkeypatch.setattr(checker.config, "load_incluster_config", MagicMock())
    monkeypatch.setattr(checker.client, "ApiClient", MagicMock())
    monkeypatch.setattr(checker.client, "AuthenticationV1Api", lambda _: api)
    return api


def provider():
    return {
        "audience": "opensandbox",
        "allowed_service_accounts": ["system:serviceaccount:osb-authn:client"],
    }


def test_allowed_decision(reviewer):
    assert checker.check("test-credential", provider()) is True
    assert reviewer.create_token_review.call_args.args[0].spec.audiences == ["opensandbox"]


@pytest.mark.parametrize(
    "authenticated,audience", [(None, "opensandbox"), (False, "opensandbox"), (True, "wrong")]
)
def test_invalid_credentials_denied(reviewer, authenticated, audience):
    result = reviewer.create_token_review.return_value.status
    result.authenticated = authenticated
    result.audiences = [audience]
    assert checker.check("test-credential", provider()) is False


def test_unlisted_identity_denied(reviewer):
    reviewer.create_token_review.return_value.status.user.username = "unlisted"
    assert checker.check("test-credential", provider()) is False


@pytest.mark.parametrize(
    "version,success",
    [
        ("v1", True),
        ("v2", False),
        (None, False),
        (1, False),
        (True, False),
    ],
)
def test_main_checks_and_echoes_version(tmp_path, monkeypatch, capsys, version, success):
    config = tmp_path / "authn.toml"
    config.write_text(
        '[provider]\naudience="opensandbox"\nallowed_service_accounts=["system:serviceaccount:osb-authn:client"]\n'
    )
    request = {"version": version, "token": "test-credential"}
    if version is None:
        del request["version"]
    validate = MagicMock(return_value=True)
    monkeypatch.setattr(checker, "check", validate)
    monkeypatch.setattr(checker.sys, "argv", ["checker", "--config", str(config)])
    monkeypatch.setattr(
        checker.sys, "stdin", SimpleNamespace(buffer=io.BytesIO(json.dumps(request).encode()))
    )
    assert checker.main() == (0 if success else 1)
    output = capsys.readouterr()
    assert "test-credential" not in output.out + output.err
    if success:
        assert json.loads(output.out) == {"version": version, "allowed": True}
        validate.assert_called_once_with("test-credential", provider())
    else:
        assert output.out == ""
        validate.assert_not_called()
