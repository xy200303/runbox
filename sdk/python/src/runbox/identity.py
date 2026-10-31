"""Runbox identity headers."""

from __future__ import annotations

import re
from dataclasses import dataclass

from .errors import IdentityError

_SCOPED = re.compile(r"^[A-Za-z0-9._-]{1,128}$")


@dataclass(frozen=True)
class Identity:
    """Either a single token or a namespace + principal pair.

    Supplying both forms, or only half of the scoped form, is an error.
    """

    token: str = ""
    namespace: str = ""
    principal: str = ""

    def __post_init__(self) -> None:
        token = self.token.strip()
        namespace = self.namespace.strip()
        principal = self.principal.strip()
        object.__setattr__(self, "token", token)
        object.__setattr__(self, "namespace", namespace)
        object.__setattr__(self, "principal", principal)

        if token and (namespace or principal):
            raise IdentityError("use either token or namespace+principal, not both")
        if token:
            if not (1 <= len(token) <= 256):
                raise IdentityError("token must be 1-256 characters")
            if any(char < "!" or char > "~" for char in token):
                raise IdentityError("token must be visible ASCII without spaces")
            return
        if not namespace or not principal:
            raise IdentityError("token or namespace+principal is required")
        if not _SCOPED.fullmatch(namespace):
            raise IdentityError("namespace must match [A-Za-z0-9._-]{1,128}")
        if not _SCOPED.fullmatch(principal):
            raise IdentityError("principal must match [A-Za-z0-9._-]{1,128}")

    def headers(self) -> dict[str, str]:
        if self.token:
            return {"X-Runbox-Identity": self.token}
        return {
            "X-Runbox-Namespace": self.namespace,
            "X-Runbox-Principal": self.principal,
        }
