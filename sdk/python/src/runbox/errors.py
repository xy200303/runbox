"""Errors raised by the Runbox client."""

from __future__ import annotations


class RunboxError(Exception):
    """An HTTP error returned by the runner."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(f"{status} {code}: {message}")
        self.status = status
        self.code = code
        self.message = message


class IdentityError(ValueError):
    """An identity is missing, ambiguous or invalid."""
