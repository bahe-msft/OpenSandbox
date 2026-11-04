# Copyright 2026 The OpenSandbox Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Run an operator-configured management authentication checker."""

import asyncio
import json
import os
from pathlib import Path
import signal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator

try:
    import tomllib  # pyright: ignore[reportMissingImports] -- Python <3.11 uses tomli below.
except ModuleNotFoundError:
    import tomli as tomllib


class AuthenticationResult(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid", frozen=True, hide_input_in_errors=True)

    allowed: bool
    subject: str | None = Field(default=None, min_length=1, max_length=256)


class ExecAuthConfig(BaseModel):
    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)

    command: list[str] = Field(min_length=1, max_length=32)
    timeout_seconds: float = Field(default=3, gt=0, le=30)

    @field_validator("command")
    @classmethod
    def validate_command(cls, command: list[str]) -> list[str]:
        if not Path(command[0]).is_absolute():
            raise ValueError("Authentication executable must use an absolute path.")
        if any(not arg or "\x00" in arg or len(arg.encode()) > 4096 for arg in command):
            raise ValueError("Invalid authentication command argument.")
        return command

    @classmethod
    def load(cls, path: Path) -> "ExecAuthConfig":
        with path.open("rb") as source:
            config = tomllib.load(source)
        # Other sections belong to the independently packaged checker.
        if "exec" not in config:
            raise ValueError("Authentication configuration requires an [exec] section.")
        return cls.model_validate(config["exec"])


class AuthenticationDenied(Exception):
    def __init__(self):
        super().__init__("Management access denied.")
        self.status_code = 401


class AuthenticationUnavailable(Exception):
    def __init__(self):
        super().__init__("Authentication checker is unavailable.")


def _unique_fields(pairs):
    fields = {}
    for key, value in pairs:
        if key in fields:
            raise ValueError("Duplicate validation result field.")
        fields[key] = value
    return fields


class ExecAuthenticator:
    MAX_OUTPUT_BYTES = 4096
    MAX_CONCURRENCY = 8

    def __init__(self, config: ExecAuthConfig):
        self.config = config
        self._slots = asyncio.Semaphore(self.MAX_CONCURRENCY)

    async def authenticate(self, token: str) -> AuthenticationResult:
        try:
            return await asyncio.wait_for(
                self._validate(token), timeout=self.config.timeout_seconds
            )
        except (OSError, asyncio.TimeoutError, ValidationError, ValueError):
            # Checker output and exception strings can contain credentials.
            raise AuthenticationUnavailable() from None

    async def _validate(self, token: str) -> AuthenticationResult:
        async with self._slots:
            process = await asyncio.create_subprocess_exec(
                *self.config.command,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL,
                start_new_session=os.name == "posix",
                limit=self.MAX_OUTPUT_BYTES + 1,
            )
            try:
                assert process.stdin is not None and process.stdout is not None
                process.stdin.write(json.dumps({"token": token}).encode() + b"\n")
                await process.stdin.drain()
                process.stdin.close()
                output = bytearray()
                while True:
                    chunk = await process.stdout.read(self.MAX_OUTPUT_BYTES + 1 - len(output))
                    if not chunk:
                        break
                    output.extend(chunk)
                    if len(output) > self.MAX_OUTPUT_BYTES:
                        raise AuthenticationUnavailable()
                if await process.wait() != 0:
                    raise AuthenticationUnavailable()
                result = AuthenticationResult.model_validate(
                    json.loads(output, object_pairs_hook=_unique_fields)
                )
                if not result.allowed:
                    raise AuthenticationDenied()
                return result
            finally:
                # POSIX checkers and their descendants share a dedicated process
                # group, including children that keep stdout open after exit.
                try:
                    if os.name == "posix":
                        os.killpg(process.pid, signal.SIGKILL)
                    elif process.returncode is None:
                        process.kill()
                except ProcessLookupError:
                    pass
                if process.stdout is not None:
                    while await process.stdout.read(self.MAX_OUTPUT_BYTES):
                        pass
                await process.wait()
