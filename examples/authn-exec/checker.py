#!/usr/bin/env python3
# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

"""Standalone TokenReview checker; no OpenSandbox imports or HTTP listener."""

import argparse
import json
from pathlib import Path
import sys

from kubernetes import client, config

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib


def check(token, provider):
    audience = provider["audience"]
    allowed = frozenset(provider["allowed_service_accounts"])
    if not audience or not allowed:
        raise ValueError("An audience and explicit allowlist are required.")
    configuration = client.Configuration()
    config.load_incluster_config(client_configuration=configuration)
    configuration.debug = False
    with client.ApiClient(configuration) as api_client:
        result = client.AuthenticationV1Api(api_client).create_token_review(
            client.V1TokenReview(
                api_version="authentication.k8s.io/v1",
                kind="TokenReview",
                spec=client.V1TokenReviewSpec(token=token, audiences=[audience]),
            ),
            _request_timeout=(1, 1),
        )
    status = result.status
    if status is None:
        raise ValueError("Missing TokenReview decision.")
    if status.authenticated is not True or audience not in (status.audiences or []):
        return {"allowed": False}
    user = status.user
    if user is None or user.username not in allowed:
        return {"allowed": False}
    return {"allowed": True, "subject": user.username}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True, type=Path)
    args = parser.parse_args()
    try:
        with args.config.open("rb") as source:
            provider = tomllib.load(source)["provider"]
        raw = sys.stdin.buffer.read(131073)
        if len(raw) > 131072:
            raise ValueError("Oversized input")
        request = json.loads(raw)
        if (
            set(request) != {"token"}
            or not isinstance(request["token"], str)
            or not 1 <= len(request["token"]) <= 16384
        ):
            raise ValueError("Invalid input")
        decision = check(request["token"], provider)
    except Exception:
        # Never echo credential-bearing Kubernetes errors or input data.
        print("Authentication check unavailable.", file=sys.stderr)
        return 1
    print(json.dumps(decision))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
