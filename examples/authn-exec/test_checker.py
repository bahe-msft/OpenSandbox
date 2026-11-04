# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

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


def test_allowed_subject(reviewer):
    assert checker.check("test-credential", provider()) == {
        "allowed": True,
        "subject": "system:serviceaccount:osb-authn:client",
    }
    assert reviewer.create_token_review.call_args.args[0].spec.audiences == ["opensandbox"]


@pytest.mark.parametrize(
    "authenticated,audience", [(None, "opensandbox"), (False, "opensandbox"), (True, "wrong")]
)
def test_invalid_credentials_denied(reviewer, authenticated, audience):
    result = reviewer.create_token_review.return_value.status
    result.authenticated = authenticated
    result.audiences = [audience]
    assert checker.check("test-credential", provider()) == {"allowed": False}


def test_unlisted_identity_denied(reviewer):
    reviewer.create_token_review.return_value.status.user.username = "unlisted"
    assert checker.check("test-credential", provider()) == {"allowed": False}
