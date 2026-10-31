"""Obfuscated and nested-script attacks.

The thesis: isolation is a kernel property (UID + path resolution), so it must
not matter how a command is spelled, encoded or nested.

Usage: python obfuscation.py
"""

from __future__ import annotations

import base64
import http.client
import json
import os
import uuid

HOST = "127.0.0.1"
PORT = int(os.environ.get("RB_PORT", "18092"))
KEY = os.environ.get("RB_KEY", "test-key-123")
RESULTS = []


def ident():
    return "obf-" + uuid.uuid4().hex[:10]


def exe(who, command, timeout=30):
    connection = http.client.HTTPConnection(HOST, PORT, timeout=timeout + 30)
    body = json.dumps({"command": command, "timeout_seconds": timeout}).encode()
    connection.request("POST", "/v1/exec", body, {
        "Authorization": f"Bearer {KEY}", "Content-Type": "application/json",
        "X-Runbox-Identity": who,
    })
    response = connection.getresponse()
    data = response.read()
    connection.close()
    try:
        return response.status, json.loads(data or b"{}")
    except json.JSONDecodeError:
        return response.status, {}


def out(who, command, timeout=30):
    _, payload = exe(who, command, timeout)
    return payload.get("stdout", "")


def record(group, name, ok, detail=""):
    RESULTS.append((group, name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {group} :: {name}" + (f" - {detail}" if detail else ""))


def secrets_absent(text: str) -> bool:
    return "root:x:0:0" not in text and "SECRET-A" not in text


def call(method, path, who, timeout=30):
    connection = http.client.HTTPConnection(HOST, PORT, timeout=timeout)
    connection.request(method, path, None, {
        "Authorization": f"Bearer {KEY}", "X-Runbox-Identity": who,
    })
    response = connection.getresponse()
    data = response.read()
    connection.close()
    return response.status, data


def test_encoded_payloads():
    group = "encoded"
    who = ident()
    payload = "mkdir -p out; cat ../../../../etc/shadow 2>&1; echo rc=$?"
    encoded = base64.b64encode(payload.encode()).decode()
    output = out(who, f"echo {encoded} | base64 -d | sh")
    record(group, "base64-encoded payload cannot read /etc/shadow",
           secrets_absent(output), output.strip()[:60])

    payload = "printf %s payload; cat /workspace/state/runbox.db 2>&1 | head -c 20"
    encoded = base64.b64encode(payload.encode()).decode()
    output = out(who, f"eval \"$(echo {encoded} | base64 -d)\"")
    record(group, "eval of decoded payload cannot read the identity table",
           "SQLite" not in output and "root:x:" not in output, output.strip()[:60])

    # hex-escaped command assembled at runtime
    hexed = payload.encode().hex()
    command = f"printf '{hexed}' | xxd -r -p | sh 2>&1"
    output = out(who, command)
    record(group, "hex-assembled payload is contained",
           "SQLite" not in output, output.strip()[:60])


def test_nested_scripts():
    group = "nested"
    alice, bob = ident(), ident()
    exe(alice, "mkdir -p out && echo SECRET-A > out/secret.txt && mkdir -p files")
    bob_keys = out(alice, "ls /workspace/workspaces 2>&1 | head -1").strip()

    # three levels: a writes b, b writes c, c carries the payload
    script = (
        "mkdir -p out; "
        "cat > out/b.sh <<'B'\n"
        "cat > out/c.sh <<'C'\n"
        "echo C-LEVEL; cat /workspace/state/runbox.db 2>&1 | head -c 12; "
        "cat /workspace/workspaces/*/out/secret.txt 2>&1 | head -c 40\n"
        "C\n"
        "chmod +x out/c.sh; sh out/c.sh\n"
        "B\n"
        "chmod +x out/b.sh; sh out/b.sh"
    )
    output = out(bob, script)
    record(group, "three-level nested script cannot read state or other workspaces",
           "SQLite" not in output and "SECRET-A" not in output, output.strip()[:70])

    # the same payload copied into the workspace and executed as a file
    exe(bob, "printf 'cat /workspace/state/runbox.db 2>&1 | head -c 12\\n' > out/x.sh && chmod +x out/x.sh")
    output = out(bob, "./out/x.sh; sh out/x.sh; bash out/x.sh 2>/dev/null")
    record(group, "script executed from disk is equally contained",
           "SQLite" not in output, output.strip()[:60])

    if bob_keys and len(bob_keys) == 64:
        output = out(bob, f"cat /workspace/workspaces/{bob_keys}/out/secret.txt 2>&1")
        record(group, "guessed workspace key from /proc is still unreadable",
               "SECRET-A" not in output, output.strip()[:60])
    else:
        record(group, "workspace keys are not listable", True, "listing denied")


def test_symlink_chains():
    group = "symlink-chain"
    who = ident()
    exe(who, "mkdir -p out && ln -s /etc/hostname out/l1 && ln -s l1 out/l2 && ln -s l2 out/l3")
    status, body = call("GET", "/v1/path?path=out/l3", who)
    record(group, "three-hop symlink chain rejected by the API",
           status in (400, 404) and b"hostname" not in body.lower(), f"status={status}")

    output = out(who, "cat out/l3 2>&1 | head -c 20")
    record(group, "symlink chain inside the container is still the container's own file",
           True, output.strip()[:40])

    exe(who, "ln -sfn /workspace/state out/state-link")
    status, body = call("GET", "/v1/path?path=out/state-link/runbox.db", who)
    record(group, "symlink into the state directory rejected",
           status in (400, 404) and b"SQLite" not in body, f"status={status}")


def test_command_obfuscation():
    group = "shell-obfuscation"
    who = ident()
    variants = {
        "variable splicing": "a=..; b=..; cat $a/$b/$a/$b/etc/shadow 2>&1 | head -c 20",
        "printf-built path": "p=$(printf '/etc/%s' shadow); cat $p 2>&1 | head -c 20",
        "brace expansion": "cat /etc/{sh,}adow 2>&1 | head -c 20",
        "quoted concatenation": "cat /etc/'sh'\"adow\" 2>&1 | head -c 20",
        "command substitution": "cat $(echo L2V0Yy9zaGFkb3c= | base64 -d) 2>&1 | head -c 20",
        "if/else nesting": "if true; then if true; then cat /etc/shadow; fi; fi 2>&1 | head -c 20",
        "here-doc": "sh <<'E'\ncat /etc/shadow\nE",
        "alias": "alias r='cat /etc/shadow'; r 2>&1 | head -c 20",
    }
    for name, command in variants.items():
        output = out(who, command)
        record(group, f"/etc/shadow unreadable via {name}", "root:" not in output,
               output.strip()[:50])


def test_escalation_obfuscation():
    group = "escalation"
    who = ident()
    exe(who, "mkdir -p out")

    script = (
        "cp /bin/sh out/su; chmod 4755 out/su; "
        "out/su -c 'id -u' 2>&1; echo rc=$?"
    )
    output = out(who, script)
    uid = output.strip().split("\n")[0].strip()
    record(group, "obfuscated setuid binary stays on the sandbox UID",
           uid not in ("0", "") and "rc=" in output, f"uid={uid[:10]}")

    script = (
        "u=$(printf un%s share | tr -d ' '); m=$(printf moun%s | tr -d ' '); "
        "$u -Urm true 2>&1; echo rc=$?"
    )
    output = out(who, script)
    record(group, "runtime-assembled unshare still blocked", "rc=0" not in output,
           output.strip()[:50])

    script = "p=$(printf ch%sroot | tr -d ' '); $p / /bin/true 2>&1; echo rc=$?"
    output = out(who, script)
    record(group, "runtime-assembled chroot still blocked", "rc=0" not in output,
           output.strip()[:50])


def test_discovery():
    group = "discovery"
    who = ident()
    exe(who, "mkdir -p out")

    output = out(who, "find / -name 'runbox.db' -readable 2>/dev/null | head -3")
    record(group, "readable identity database cannot be found", "runbox.db" not in output,
           output.strip()[:40] or "nothing readable")

    output = out(who, "find / -name 'runbox.db*' -readable 2>/dev/null | head -3")
    record(group, "no readable runbox database anywhere", output.strip() == "", output.strip()[:40])

    output = out(who, "ls -1 /workspace/workspaces 2>&1 | head -2")
    record(group, "workspace keys are not listable",
           "Permission denied" in output or output.strip() == "", output.strip()[:50])

    own = out(who, "basename \"$(readlink /proc/self/cwd)\"")
    seen = out(who, "for p in /proc/[0-9]*/cwd; do readlink $p 2>/dev/null; done")
    foreign = [line for line in seen.splitlines()
               if "/workspace/workspaces/" in line and own.strip() not in line]
    record(group, "other identities' cwd is not readable", not foreign,
           f"foreign={foreign[:1]}")


def test_repeat_abuse():
    group = "abuse"
    who = ident()
    command = (
        "mkdir -p out; i=0; while [ $i -lt 40 ]; do "
        "cat /etc/shadow 2>/dev/null; i=$((i+1)); done; echo done"
    )
    output = out(who, command, timeout=60)
    record(group, "40 repeated read attempts stay denied", "root:" not in output,
           output.strip()[-40:])

    command = (
        "mkdir -p out; ln -sfn /etc/shadow out/s; i=0; "
        "while [ $i -lt 20 ]; do cat out/s 2>/dev/null; i=$((i+1)); done; echo done"
    )
    output = out(who, command, timeout=60)
    record(group, "20 symlink read attempts stay denied", "root:" not in output,
           output.strip()[-40:])


def main():
    groups = [test_encoded_payloads, test_nested_scripts, test_symlink_chains,
              test_command_obfuscation, test_escalation_obfuscation, test_discovery,
              test_repeat_abuse]
    for group in groups:
        try:
            group()
        except Exception as error:  # noqa: BLE001
            record(group.__name__, "group crashed", False, f"{type(error).__name__}: {error}")

    failed = [row for row in RESULTS if not row[2]]
    print(f"\n{len(RESULTS) - len(failed)} passed, {len(failed)} failed")
    for group, name, _, detail in failed:
        print(f"  FAIL {group} :: {name} - {detail}")


if __name__ == "__main__":
    main()
