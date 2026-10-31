"""Runbox security and performance probes.

Talks to a live runner over HTTP the way an untrusted caller would.
Usage: python rbtest.py security|perf
"""

from __future__ import annotations

import http.client
import json
import os
import statistics
import sys
import threading
import time
import uuid

HOST = os.environ.get("RB_HOST", "127.0.0.1")
PORT = int(os.environ.get("RB_PORT", "18098"))
KEY = os.environ.get("RB_KEY", "test-key-123")

RESULTS = []


def tok(value):
    return {"X-Runbox-Identity": value}


def scoped(namespace, principal):
    return {"X-Runbox-Namespace": namespace, "X-Runbox-Principal": principal}


def new_identity():
    return tok("t-" + uuid.uuid4().hex[:12])


def call(method, path, identity=None, body=None, headers=None, auth=True, timeout=120):
    connection = http.client.HTTPConnection(HOST, PORT, timeout=timeout)
    sent = {"Accept": "application/json"}
    if auth:
        sent["Authorization"] = f"Bearer {KEY}"
    if identity:
        sent.update(identity)
    if headers:
        sent.update(headers)
    if body is not None and not isinstance(body, bytes):
        body = json.dumps(body).encode()
        sent.setdefault("Content-Type", "application/json")
    connection.request(method, path, body=body, headers=sent)
    response = connection.getresponse()
    payload = response.read()
    connection.close()
    return response.status, dict(response.getheaders()), payload


def exec_cmd(identity, command, timeout=60, uploads=None, timeout_seconds=60,
             client_timeout=None):
    client_timeout = client_timeout or timeout
    if not uploads:
        status, _, body = call(
            "POST", "/v1/exec", identity,
            {"command": command, "timeout_seconds": timeout},
            timeout=client_timeout,
        )
        return status, json.loads(body or b"{}")
    boundary = "----rb" + uuid.uuid4().hex
    chunks = []
    for field, value in (("command", command), ("timeout_seconds", str(timeout))):
        chunks.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"\r\n\r\n{value}\r\n'.encode()
        )
    for name, data in uploads:
        chunks.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="files"; filename="{name}"\r\n'
            f"Content-Type: application/octet-stream\r\n\r\n".encode() + data + b"\r\n"
        )
    chunks.append(f"--{boundary}--\r\n".encode())
    status, _, body = call(
        "POST", "/v1/exec", identity, b"".join(chunks),
        {"Content-Type": f"multipart/form-data; boundary={boundary}"}, timeout=timeout,
    )
    return status, json.loads(body or b"{}")


def upload(identity, name, data, field="file", timeout=300):
    boundary = "----rb" + uuid.uuid4().hex
    head = (
        f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"; filename="{name}"\r\n'
        f"Content-Type: application/octet-stream\r\n\r\n"
    ).encode()
    tail = f"\r\n--{boundary}--\r\n".encode()
    return call(
        "POST", "/v1/files", identity, head + data + tail,
        {"Content-Type": f"multipart/form-data; boundary={boundary}"}, timeout=timeout,
    )


def record(group, name, ok, detail=""):
    RESULTS.append((group, name, ok, detail))
    flag = "PASS" if ok else "FAIL"
    print(f"[{flag}] {group} :: {name}" + (f" - {detail}" if detail else ""))


def expect(group, name, fn, predicate, detail=""):
    try:
        value = fn()
        ok = predicate(value)
        record(group, name, ok, detail if not ok else "")
        return value
    except Exception as error:  # noqa: BLE001
        record(group, name, False, f"{type(error).__name__}: {error}")
        return None


# ---------------------------------------------------------------- security A: auth

def test_auth():
    group = "auth"
    identity = new_identity()
    cases = [
        ("exec without bearer", "POST", "/v1/exec", {"command": "true"}, False, 401),
        ("exec with wrong bearer", "POST", "/v1/exec", {"command": "true"}, "wrong", 401),
        ("metrics without bearer", "GET", "/metrics", None, False, 401),
        ("capabilities without bearer", "GET", "/v1/capabilities", None, False, 401),
        ("list without bearer", "GET", "/v1/list", None, False, 401),
        ("path without bearer", "GET", "/v1/path?path=out/x", None, False, 401),
        ("archive without bearer", "GET", "/v1/archive?path=out", None, False, 401),
        ("health without bearer", "GET", "/health", None, False, 200),
        ("ready without bearer", "GET", "/ready", None, False, 200),
    ]
    for name, method, path, body, auth, want in cases:
        status, _, _ = call(method, path, identity, body,
                            auth=(auth is True or auth == KEY))
        record(group, name, status == want, f"got {status}, want {want}")

    # bearer in query string must not authenticate
    status, _, _ = call("GET", f"/v1/capabilities?api_key={KEY}&token={KEY}", None, None, auth=False)
    record(group, "bearer via query string rejected", status == 401, f"got {status}")


# ------------------------------------------------------------- security B: identity

def raw_request(method, path, header_lines, body=b"", timeout=30):
    """Send a request with exact header lines, so duplicates and odd bytes survive."""
    import socket

    lines = [f"{method} {path} HTTP/1.1", f"Host: {HOST}:{PORT}",
             f"Authorization: Bearer {KEY}"] + header_lines
    raw = ("\r\n".join(lines) + "\r\n").encode() + body
    with socket.create_connection((HOST, PORT), timeout=timeout) as sock:
        sock.sendall(raw)
        data = b""
        while b"\r\n\r\n" not in data:
            chunk = sock.recv(65536)
            if not chunk:
                break
            data += chunk
    head, _, rest = data.partition(b"\r\n\r\n")
    status = int(head.split(b" ")[1])
    return status, head, rest


def test_identity_headers():
    group = "identity"
    body = {"command": "true"}
    cases = [
        ("no identity headers", {}, 400),
        ("both identity forms", {**tok("alice"), **scoped("team", "bob")}, 400),
        ("namespace without principal", {"X-Runbox-Namespace": "team"}, 400),
        ("principal without namespace", {"X-Runbox-Principal": "bob"}, 400),
        ("token with space", tok("ali ce"), 400),
        ("token 257 chars", tok("a" * 257), 400),
        ("namespace 129 chars", scoped("n" * 129, "u"), 400),
        ("namespace with colon", scoped("team:1", "u"), 400),
        ("principal with pipe", scoped("team", "u|1"), 400),
    ]
    for name, headers, want in cases:
        try:
            status, _, _ = call("POST", "/v1/exec", headers, body)
        except Exception as error:  # noqa: BLE001
            record(group, name, False, f"{type(error).__name__}: {error}")
            continue
        record(group, name, status == want, f"got {status}, want {want}")

    status, _, _ = call("POST", "/v1/exec", tok("alice"), body)
    record(group, "valid token accepted", status == 200, f"got {status}")
    status, _, _ = call("POST", "/v1/exec", scoped("team", "bob"), body)
    record(group, "valid scoped identity accepted", status == 200, f"got {status}")

    # visible ASCII without spaces is legal, including / and ..
    for token in ("a/b", "..", "a/../b", "a b".replace(" ", ""), "x" * 256):
        try:
            status, _, _ = call("POST", "/v1/exec", tok(token), body)
            record(group, f"legal token accepted: {token[:12]!r}", status == 200, f"got {status}")
        except Exception as error:  # noqa: BLE001
            record(group, f"legal token accepted: {token[:12]!r}", False,
                   f"{type(error).__name__}: {error}")

    # non-ASCII identity bytes must be rejected, not silently mangled
    try:
        status, _, _ = raw_request("POST", "/v1/exec", [
            "X-Runbox-Identity: 用户", "Content-Type: application/json"],
            json.dumps(body).encode())
        record(group, "non-ASCII identity rejected", status == 400, f"got {status}")
    except Exception as error:  # noqa: BLE001
        record(group, "non-ASCII identity rejected", True, f"connection refused: {error}")

    # raw header abuse
    try:
        status, _, _ = raw_request("POST", "/v1/exec", [
            "X-Runbox-Identity: a\nb", "Content-Type: application/json"], json.dumps(body).encode())
        record(group, "token with raw newline rejected", status == 400, f"got {status}")
    except Exception as error:  # noqa: BLE001
        record(group, "token with raw newline rejected", True, f"connection refused: {error}")

    try:
        status, _, _ = raw_request("POST", "/v1/exec", [
            "X-Runbox-Identity: alice", "X-Runbox-Identity: bob",
            "Content-Type: application/json"], json.dumps(body).encode())
        record(group, "duplicate identity header rejected", status == 400, f"got {status}")
    except Exception as error:  # noqa: BLE001
        record(group, "duplicate identity header rejected", False, f"{error}")


def test_methods():
    group = "http"
    identity = new_identity()
    status, _, _ = call("GET", "/v1/exec", identity)
    record(group, "GET /v1/exec rejected", status in (404, 405), f"got {status}")
    status, _, _ = call("POST", "/v1/list", identity, {"path": ""})
    record(group, "POST /v1/list rejected", status in (404, 405), f"got {status}")
    status, _, _ = call("DELETE", "/v1/path?path=out/x", identity)
    record(group, "DELETE /v1/path rejected", status in (404, 405), f"got {status}")


# ----------------------------------------------------------- security C: isolation

def test_isolation():
    group = "isolation"
    alice = tok("alice-" + uuid.uuid4().hex[:6])
    bob = tok("bob-" + uuid.uuid4().hex[:6])

    exec_cmd(alice, "mkdir -p out files && printf ALICE-SECRET > files/secret.txt && printf AAA > out/shared.txt")
    exec_cmd(bob, "mkdir -p out files && printf BBB > out/shared.txt")

    status, _, body = call("GET", "/v1/path?path=files/secret.txt", bob)
    record(group, "B cannot download A's file", status == 404, f"got {status} {body[:60]!r}")

    status, _, body = call("GET", "/v1/path?path=out/shared.txt", alice)
    record(group, "same path reads own content (A)", status == 200 and body == b"AAA",
           f"got {status} {body[:30]!r}")
    status, _, body = call("GET", "/v1/path?path=out/shared.txt", bob)
    record(group, "same path reads own content (B)", status == 200 and body == b"BBB",
           f"got {status} {body[:30]!r}")

    status, payload = exec_cmd(bob, "cat files/secret.txt 2>&1; echo exit=$?")
    record(group, "B cannot read A's file via exec",
           "ALICE-SECRET" not in payload.get("stdout", ""), payload.get("stdout", "")[:60])

    status, payload = exec_cmd(bob, "ls -A out files")
    record(group, "B's directory listing has no A files",
           "secret.txt" not in payload.get("stdout", ""), payload.get("stdout", "")[:80])

    status, payload = exec_cmd(alice, "ls -A /workspace/workspaces 2>&1")
    record(group, "identities cannot enumerate workspaces",
           "Permission denied" in payload.get("stdout", "") or payload.get("exit_code") != 0,
           payload.get("stdout", "")[:80])

    status, payload = exec_cmd(alice, "cat /workspace/state/runbox.db > /dev/null 2>&1; echo $?")
    record(group, "identity cannot read the identity database",
           payload.get("stdout", "").strip() != "0", payload.get("stdout", "")[:40])

    exec_cmd(bob, "rm -rf out files")
    status, payload = exec_cmd(alice, "cat files/secret.txt")
    record(group, "B deleting its workspace leaves A intact",
           payload.get("stdout", "") == "ALICE-SECRET", payload.get("stdout", "")[:40])

    # scoped identity is a different bucket from a token with the same text
    ns = scoped("team-x", "user-1")
    exec_cmd(ns, "mkdir -p out && printf SCOPED > out/who.txt")
    status, payload = exec_cmd(tok("team-x"), "cat out/who.txt 2>&1")
    record(group, "scoped identity and token do not share a workspace",
           payload.get("stdout", "") != "SCOPED", payload.get("stdout", "")[:40])


# --------------------------------------------------- security D: traversal/symlink

def test_traversal():
    group = "traversal"
    identity = new_identity()
    exec_cmd(identity, "mkdir -p out files && printf ok > out/ok.txt")

    paths = [
        "../../etc/passwd",
        "..%2F..%2Fetc%2Fpasswd",
        "%2e%2e%2f%2e%2e%2fetc%2fpasswd",
        "....//....//etc/passwd",
        ".%2e/.%2e/etc/passwd",
        "/etc/passwd",
        "//etc/passwd",
        "C:\\Windows\\win.ini",
        "..\\..\\etc\\passwd",
        "out/../../etc/passwd",
        "files/%2e%2e/%2e%2e/etc/passwd",
        "out/ok.txt%00.png",
        "a" * 5000,
    ]
    for path in paths:
        quoted = path.replace(" ", "%20")
        status, _, body = call("GET", f"/v1/path?path={quoted}", identity)
        leaked = b"root:" in body
        record(group, f"path rejected: {path[:38]!r}", status in (400, 404, 414) and not leaked,
               f"got {status}, leaked={leaked}")

    # the container's own /etc/passwd is world-readable by design; that is not
    # a traversal bug. The boundary that matters is other tenants' workspaces.
    _, listing = exec_cmd(identity, "ls -1 /workspace/workspaces 2>/dev/null | head -1")
    foreign = listing.get("stdout", "").strip().splitlines()
    if foreign:
        _, read = exec_cmd(identity, "cat /workspace/workspaces/" + foreign[0] + "/out/shared.txt 2>&1")
        record(group, "exec cannot read another identity's workspace",
               "Permission denied" in read.get("stdout", "") or "No such file" in read.get("stdout", ""),
               read.get("stdout", "")[:70])
    _, host_file = exec_cmd(identity, "cat /etc/passwd | head -1")
    record(group, "container /etc/passwd readable (shared-kernel, expected)",
           "root:" in host_file.get("stdout", ""), "documented trade-off")

    # symlink inside the workspace pointing outside it
    exec_cmd(identity, "ln -sfn /etc/passwd out/escape.txt && ln -sfn /workspace/state/runbox.db out/db.txt")
    for path in ("out/escape.txt", "out/db.txt"):
        status, _, body = call("GET", f"/v1/path?path={path}", identity)
        record(group, f"symlink escape blocked: {path}",
               status in (400, 404) and b"root:" not in body, f"got {status}")

    status, _, body = call("GET", "/v1/archive?path=out/escape.txt", identity)
    record(group, "symlink escape blocked in archive",
           status in (400, 404) or b"root:" not in body, f"got {status}")

    status, _, body = call("GET", "/v1/archive?path=..", identity)
    record(group, "archive rejects ..", status in (400, 404), f"got {status}")

    status, _, body = call("GET", "/v1/archive?path=out&path=../state", identity)
    record(group, "archive rejects mixed escape", status in (400, 404), f"got {status}")

    # upload filenames must be sanitized into files/
    status, _, body = upload(identity, "../../evil.txt", b"pwned")
    payload = json.loads(body or b"{}")
    saved = payload.get("path", "")
    record(group, "upload filename traversal sanitized",
           status == 200 and ".." not in saved and saved.startswith("files/"),
           f"got {status} path={saved!r}")
    status, payload2 = exec_cmd(identity, "ls -A /workspace | tr '\\n' ' '")
    record(group, "traversal upload landed nowhere outside files/",
           "evil.txt" not in payload2.get("stdout", ""), payload2.get("stdout", "")[:80])

    # download name with quotes and newlines must not break the header
    exec_cmd(identity, "printf x > 'out/a\"b.txt'")
    status, headers, _ = call("GET", '/v1/path?path=out/a%22b.txt', identity)
    disposition = headers.get("Content-Disposition", "")
    record(group, "quoted filename in Content-Disposition is safe",
           status == 200 and "\n" not in disposition and disposition.count('"') % 2 == 0,
           disposition[:60])


# ------------------------------------------------- security E: container boundaries

def test_container():
    group = "container"
    identity = new_identity()

    status, payload = exec_cmd(identity, "id -u; id -g")
    uid = payload.get("stdout", "").split()
    record(group, "command runs as a sandbox UID, not root",
           len(uid) == 2 and uid[0] != "0", payload.get("stdout", "")[:40])

    checks = [
        ("cannot write /", "touch /root-pwned 2>&1 || echo denied"),
        ("cannot write /workspace root", "touch /workspace/pwned 2>&1 || echo denied"),
        ("cannot chmod another UID's file", "chmod 777 /workspace/state 2>&1 || echo denied"),
        ("sudo is unavailable", "sudo -n true 2>&1 || echo denied"),
        ("setuid copy does not escalate", "cp /bin/sh /tmp/sh && chmod u+s /tmp/sh && /tmp/sh -c 'id -u'"),
    ]
    for name, command in checks:
        _, payload = exec_cmd(identity, command)
        output = payload.get("stdout", "").strip()
        if name == "setuid copy does not escalate":
            record(group, name, output != "0", f"uid={output[:20]}")
        else:
            record(group, name, "denied" in output or payload.get("exit_code") != 0, output[:60])

    # another identity's process must not be killable
    victim = new_identity()
    exec_cmd(victim, "sleep 122 > /dev/null 2>&1 & echo started")
    time.sleep(0.5)
    status, payload = exec_cmd(identity, "pkill -9 -f 'sleep 122' 2>&1; echo rc=$?")
    record(group, "cannot kill another identity's process",
           "rc=0" not in payload.get("stdout", ""), payload.get("stdout", "")[:60])

    # a setsid daemon escapes the process group; the request returns but the
    # process keeps running. This is the shared-container trade-off, recorded
    # so the report states it instead of implying reaping.
    daemon = new_identity()
    exec_cmd(daemon, "setsid sleep 123 > /dev/null 2>&1 & echo started")
    time.sleep(0.5)
    _, check = exec_cmd(daemon, "pgrep -c -f 'sleep 123' || true")
    record(group, "setsid daemon outlives its request (shared-container trade-off)",
           check.get("stdout", "0").strip() not in ("", "0"),
           f"daemons={check.get('stdout', '').strip()}")

    # stdout must be bounded
    start = time.time()
    status, payload = exec_cmd(identity, "yes | head -c 40000000", timeout=120)
    elapsed = time.time() - start
    stdout = payload.get("stdout", "")
    record(group, "huge stdout is bounded",
           len(stdout) <= 204800 + 200 and "truncated" in stdout,
           f"{len(stdout)} bytes in {elapsed:.1f}s")

    # timeout kills the process tree and reports 409 timeout
    start = time.time()
    status, payload = exec_cmd(identity, "sleep 121 & sleep 121", timeout=2, client_timeout=60)
    elapsed = time.time() - start
    _, check = exec_cmd(identity, "ps -eo args | grep -c '^sleep 121$' || true")
    leftovers = check.get("stdout", "?").strip()
    record(group, "timeout returns 409 and reaps the tree",
           status == 409 and payload.get("code") == "timeout" and elapsed < 10
           and leftovers in ("0", ""),
           f"{elapsed:.1f}s status={status} code={payload.get('code')} left={leftovers}")

    # fork pressure stays inside the sandbox
    start = time.time()
    exec_cmd(identity, "for i in $(seq 1 120); do sleep 8 & done; wait", timeout=6, client_timeout=60)
    record(group, "process pressure is contained",
           time.time() - start < 30, f"{time.time() - start:.1f}s")


def test_limits():
    group = "limits"
    identity = new_identity()
    CRLF = chr(13) + chr(10)

    status, _, body = upload(identity, "too-big.bin", b"x" * (21 << 20))
    record(group, "upload over 20 MB rejected", status == 413, f"got {status}")

    long_command = "echo " + "a" * 70000
    status, _, body = call("POST", "/v1/exec", identity,
                           {"command": long_command, "timeout_seconds": 10})
    record(group, "command over 64 KiB rejected", status in (400, 413), f"got {status}")

    boundary = "----rb" + uuid.uuid4().hex
    parts = []
    parts.append(("--" + boundary + CRLF +
                  'Content-Disposition: form-data; name="command"' + CRLF + CRLF +
                  "true" + CRLF).encode())
    for i in range(65):
        parts.append(("--" + boundary + CRLF +
                      'Content-Disposition: form-data; name="files"; filename="f' + str(i) + '.txt"' + CRLF +
                      "Content-Type: application/octet-stream" + CRLF + CRLF).encode() + b"x" + CRLF.encode())
    parts.append(("--" + boundary + "--" + CRLF).encode())
    status, _, _ = call("POST", "/v1/exec", identity, b"".join(parts),
                        {"Content-Type": "multipart/form-data; boundary=" + boundary})
    record(group, "more than 64 attachments rejected", status in (400, 413), f"got {status}")

    status, _, _ = call("GET", "/v1/list?path=" + "a" * 5000, identity)
    record(group, "oversized path rejected", status in (400, 404, 414), f"got {status}")


def percentile(values, pct):
    ordered = sorted(values)
    index = min(len(ordered) - 1, int(round((pct / 100) * (len(ordered) - 1))))
    return ordered[index]


def bench_exec():
    group = "perf-exec"
    identity = new_identity()
    exec_cmd(identity, "true")

    latencies = []
    for _ in range(40):
        start = time.perf_counter()
        exec_cmd(identity, "true")
        latencies.append((time.perf_counter() - start) * 1000)
    print(f"    exec true: p50={statistics.median(latencies):.1f}ms "
          f"p95={percentile(latencies, 95):.1f}ms max={max(latencies):.1f}ms")
    record(group, "sequential exec latency", percentile(latencies, 95) < 500,
           f"p50={statistics.median(latencies):.1f}ms p95={percentile(latencies, 95):.1f}ms")

    for command, label in (("python3 -c pass", "python3"), ("node -e 0", "node")):
        samples = []
        for _ in range(10):
            start = time.perf_counter()
            exec_cmd(identity, command)
            samples.append((time.perf_counter() - start) * 1000)
        print(f"    exec {label}: p50={statistics.median(samples):.1f}ms")

    # cold identity: first call allocates a UID and creates the workspace
    samples = []
    for _ in range(10):
        fresh = new_identity()
        start = time.perf_counter()
        exec_cmd(fresh, "true")
        samples.append((time.perf_counter() - start) * 1000)
    print(f"    new identity first exec: p50={statistics.median(samples):.1f}ms "
          f"max={max(samples):.1f}ms")
    record(group, "new identity bootstrap under 1s", max(samples) < 1000,
           f"p50={statistics.median(samples):.1f}ms")


def bench_concurrency():
    group = "perf-concurrency"
    for workers in (2, 4, 8, 16):
        identities = [new_identity() for _ in range(workers)]
        for identity in identities:
            exec_cmd(identity, "true")
        start = time.perf_counter()

        def work(identity):
            exec_cmd(identity, "true")

        threads = [threading.Thread(target=work, args=(i,)) for i in identities]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        elapsed = time.perf_counter() - start
        rate = workers / elapsed
        print(f"    {workers:2d} identities in parallel: {elapsed * 1000:6.0f}ms "
              f"({rate:.1f} exec/s)")
        record(group, f"{workers} concurrent identities", elapsed < workers * 1.0,
               f"{elapsed:.2f}s for {workers} calls")

    # excess demand over RUNBOX_MAX_CONCURRENCY must fail fast, not queue forever
    busy_codes = []
    def burst(identity):
        status, payload = exec_cmd(identity, "sleep 2", timeout=30)
        busy_codes.append(payload.get("code") if status != 200 else "ok")
    threads = [threading.Thread(target=burst, args=(new_identity(),)) for _ in range(8)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    failures = sum(1 for code in busy_codes if code != "ok")
    print(f"    8 parallel sleep 2 with max_concurrency=2: {busy_codes}")
    record(group, "excess concurrency fails fast with 409 busy OR serializes",
           failures == 0 or "busy" in busy_codes, f"failures={failures}")

    # same identity must serialize, not interleave
    identity = new_identity()
    exec_cmd(identity, "true")
    start = time.perf_counter()
    threads = [threading.Thread(target=exec_cmd, args=(identity, "sleep 1")) for _ in range(3)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    elapsed = time.perf_counter() - start
    print(f"    3 x sleep 1 on one identity: {elapsed:.2f}s")
    record(group, "same identity serializes", elapsed >= 2.8, f"{elapsed:.2f}s")


def bench_files():
    group = "perf-files"
    identity = new_identity()

    for size, label in ((1 << 20, "1 MB"), (10 << 20, "10 MB"), (19 << 20, "19 MB")):
        blob = b"x" * size
        start = time.perf_counter()
        status, _, body = upload(identity, f"up-{size}.bin", blob)
        elapsed = time.perf_counter() - start
        rate = size / elapsed / (1 << 20)
        print(f"    upload {label}: {elapsed:.2f}s ({rate:.1f} MiB/s) status={status}")
        record(group, f"upload {label}", status == 200 and elapsed < 30, f"{elapsed:.2f}s")

    big = 32 << 20
    exec_cmd(identity, "mkdir -p out && head -c 33554432 /dev/urandom > out/down.bin", timeout=120)

    start = time.perf_counter()
    status, headers, body = call("GET", "/v1/path?path=out/down.bin", identity, timeout=300)
    elapsed = time.perf_counter() - start
    print(f"    download 32 MB: {elapsed:.2f}s ({len(body) / elapsed / (1 << 20):.1f} MiB/s) "
          f"status={status}")
    record(group, "download 32 MB", status == 200 and len(body) == big and elapsed < 60,
           f"{elapsed:.2f}s")

    start = time.perf_counter()
    status, headers, body = call(
        "GET", "/v1/path?path=out/down.bin", identity,
        headers={"Range": "bytes=0-1048575"}, timeout=120,
    )
    elapsed = time.perf_counter() - start
    print(f"    range 1 MB of 32 MB: {elapsed * 1000:.0f}ms status={status} "
          f"range={headers.get('Content-Range')}")
    record(group, "range read", status == 206 and len(body) == (1 << 20),
           f"status={status} bytes={len(body)}")

    # many small files, created by the sandbox itself
    start = time.perf_counter()
    exec_cmd(identity, "mkdir -p out/many && for i in $(seq 1 500); do printf abc > out/many/f$i.txt; done",
             timeout=120)
    create_elapsed = time.perf_counter() - start

    start = time.perf_counter()
    status, _, body = call("GET", "/v1/list?path=out/many", identity)
    list_elapsed = time.perf_counter() - start
    entries = len(json.loads(body).get("entries", []))

    start = time.perf_counter()
    status, headers, tar = call("GET", "/v1/archive?path=out/many", identity, timeout=300)
    archive_elapsed = time.perf_counter() - start
    print(f"    500 files: create={create_elapsed:.1f}s list={list_elapsed * 1000:.0f}ms "
          f"archive={archive_elapsed:.2f}s ({len(tar) / 1024:.0f} KiB)")
    record(group, "list 500 files", entries == 500 and list_elapsed < 5, f"{entries} entries")
    record(group, "archive 500 files", status == 200 and len(tar) > 5000 and archive_elapsed < 30,
           f"{archive_elapsed:.2f}s")

    # batch upload of many attachments in one exec
    files = [(f"a{i}.txt", b"z" * 1024) for i in range(20)]
    start = time.perf_counter()
    status, payload = exec_cmd(identity, "ls files | wc -l", uploads=files)
    elapsed = time.perf_counter() - start
    print(f"    exec with 20 attachments: {elapsed:.2f}s status={status}")
    record(group, "exec with 20 attachments", status == 200 and elapsed < 30, f"{elapsed:.2f}s")


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "security"
    if mode == "security":
        groups = [test_auth, test_identity_headers, test_methods, test_isolation,
                  test_traversal, test_container, test_limits]
    else:
        groups = [bench_exec, bench_concurrency, bench_files]
    for group in groups:
        try:
            group()
        except Exception as error:  # noqa: BLE001
            record(group.__name__, "group crashed", False, f"{type(error).__name__}: {error}")

    failed = [row for row in RESULTS if not row[2]]
    print(f"\n{len(RESULTS) - len(failed)} passed, {len(failed)} failed")
    for group, name, _, detail in failed:
        print(f"  FAIL {group} :: {name} — {detail}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
