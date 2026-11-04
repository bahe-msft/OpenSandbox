# Copyright 2026 The OpenSandbox Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

"""Exec-authentication smoke tests; never print credentials or response bodies."""

import argparse
from pathlib import Path
import time

import httpx

BASE = "http://opensandbox-server:8080"
TOKEN = Path("/run/identity/token")


class TokenFileAuth(httpx.Auth):
    def __init__(self, path):
        self.path = path

    def auth_flow(self, request):
        request.headers["Authorization"] = f"Bearer {self.path.read_text().strip()}"
        yield request


def check(response, status, label):
    if response.status_code != status:
        raise SystemExit(f"FAIL {label}: expected {status}, received {response.status_code}")
    print(f"PASS {label}: {status}", flush=True)


def smoke():
    with httpx.Client(base_url=BASE, timeout=10) as client:
        check(client.get("/health"), 200, "public health")
        check(client.get("/v1/pools"), 401, "missing credential")
        check(
            client.get("/v1/pools", headers={"OPEN-SANDBOX-API-KEY": "old-key"}),
            401,
            "no API-key fallback",
        )
        check(
            client.get("/v1/pools", headers={"Authorization": "Bearer invalid"}),
            401,
            "invalid credential",
        )
        check(
            client.get("/v1/pools", auth=TokenFileAuth(Path("/run/identity/wrong-audience"))),
            401,
            "wrong audience",
        )
        response = client.get("/v1/pools", auth=TokenFileAuth(TOKEN))
        check(response, 200, "exec allow decision")
        if response.json() != {"items": []}:
            raise SystemExit("FAIL expected the dedicated cluster's empty pool list")
        check(client.get("/health/private"), 401, "exact public-path exemption")


def probe(expected):
    deadline = time.monotonic() + 40
    with httpx.Client(base_url=BASE, timeout=10, auth=TokenFileAuth(TOKEN)) as client:
        while True:
            response = client.get("/v1/pools")
            if response.status_code == expected:
                check(response, expected, "checker decision")
                return
            if time.monotonic() >= deadline:
                check(response, expected, "checker decision")
            time.sleep(1)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["smoke", "denied", "unavailable", "recovered"])
    args = parser.parse_args()
    if args.mode == "smoke":
        smoke()
    else:
        probe({"denied": 401, "unavailable": 503, "recovered": 200}[args.mode])
