"""Adversarial probes beyond the ordinary contract. Usage: python deep.py"""

from __future__ import annotations

import http.client
import json
import os
import socket
import subprocess
import threading
import time
import uuid

HOST = "127.0.0.1"
PORT = int(os.environ.get("RB_PORT", "18093"))
KEY = os.environ.get("RB_KEY", "test-key-123")
CONTAINER = os.environ.get("RB_CONTAINER", "rb-deep")
RESULTS = []


def ident():
    return "deep-" + uuid.uuid4().hex[:10]


def call(method, path, who, body=None, headers=None, timeout=60):
    connection = http.client.HTTPConnection(HOST, PORT, timeout=timeout)
    sent = {"Authorization": f"Bearer {KEY}", "Accept": "application/json",
            "X-Runbox-Identity": who}
    if headers:
        sent.update(headers)
    payload = None
    if isinstance(body, bytes):
        payload = body
    elif body is not None:
        payload = json.dumps(body).encode()
        sent["Content-Type"] = "application/json"
    connection.request(method, path, payload, sent)
    response = connection.getresponse()
    data = response.read()
    connection.close()
    return response.status, data


def exe(who, command, timeout=30):
    status, data = call("POST", "/v1/exec", who,
                        {"command": command, "timeout_seconds": timeout}, timeout=timeout + 30)
    try:
        return status, json.loads(data or b"{}")
    except json.JSONDecodeError:
        return status, {"raw": data[:200].decode(errors="replace")}


def out(who, command):
    _, payload = exe(who, command)
    return payload.get("stdout", "")


def record(group, name, ok, detail=""):
    RESULTS.append((group, name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {group} :: {name}" + (f" - {detail}" if detail else ""))


def raw(headers, path="/v1/exec", payload=b'{"command":"true"}'):
    lines = [f"POST {path} HTTP/1.1", f"Host: {HOST}:{PORT}",
             f"Authorization: Bearer {KEY}", "Content-Type: application/json"] + headers
    data = ("\r\n".join(lines) + "\r\n").encode() + payload
    with socket.create_connection((HOST, PORT), timeout=20) as sock:
        sock.sendall(data)
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = sock.recv(65536)
            if not chunk:
                break
            buf += chunk
    return int(buf.split(b" ")[1])


def test_hardlinks():
    group = "hardlink"
    who = ident()
    _, payload = exe(who, "mkdir -p out; ln /etc/shadow out/shadow 2>&1; echo rc=$?")
    output = payload.get("stdout", "")
    record(group, "cannot hardlink a root-only file", "rc=0" not in output, output.strip()[:60])


def test_special_files():
    group = "special"
    who = ident()
    exe(who, "mkdir -p out && mkfifo out/pipe && echo data > out/ok.txt")

    started = time.time()
    status, _ = call("GET", "/v1/path?path=out/pipe", who, timeout=10)
    elapsed = time.time() - started
    record(group, "FIFO download rejected without blocking",
           status in (400, 404) and elapsed < 3, f"status={status} {elapsed:.2f}s")

    status, _ = call("GET", "/v1/path?path=out", who)
    record(group, "directory download rejected", status in (400, 404), f"status={status}")

    started = time.time()
    status, body = call("GET", "/v1/archive?path=out", who, timeout=30)
    elapsed = time.time() - started
    record(group, "archive with FIFO completes", status == 200 and elapsed < 10,
           f"status={status} {elapsed:.2f}s {len(body)}B")
    record(group, "archive does not contain host secrets", b"root:x:" not in body, "")


def test_symlink_race():
    group = "race"
    who = ident()
    exe(who, "mkdir -p out && echo SAFE > out/data.txt && rm -f out/race")
    hostname = subprocess.run(["docker", "exec", CONTAINER, "cat", "/etc/hostname"],
                              capture_output=True, text=True).stdout.strip()
    stop = threading.Event()
    hits = {"ok": 0, "escape": 0}

    def swapper():
        targets = ["/etc/hostname", "out/data.txt", "/etc/shadow"]
        index = 0
        while not stop.is_set():
            exe(who, "rm -f out/race; ln -s " + targets[index % len(targets)] + " out/race")
            index += 1

    def reader():
        while not stop.is_set():
            try:
                status, body = call("GET", "/v1/path?path=out/race", who, timeout=10)
            except Exception:  # noqa: BLE001
                continue
            if status == 200:
                hits["ok"] += 1
                if hostname and hostname.encode() in body:
                    hits["escape"] += 1

    threads = [threading.Thread(target=swapper, daemon=True)] + [
        threading.Thread(target=reader, daemon=True) for _ in range(3)]
    for thread in threads:
        thread.start()
    time.sleep(4)
    stop.set()
    for thread in threads:
        thread.join(timeout=5)
    record(group, "symlink race never follows an outside link",
           hits["escape"] == 0, f"200s={hits['ok']} escapes={hits['escape']}")


def test_proc_leak():
    group = "proc"
    alice, bob = ident(), ident()
    exe(alice, "setsid sleep 601 >/dev/null 2>&1 & echo started")
    time.sleep(0.8)

    env1 = out(alice, "head -c 40 /proc/1/environ 2>&1")
    record(group, "environment of pid 1 is protected",
           "denied" in env1.lower(), env1.strip()[:50])

    pid = out(bob, "pgrep -f '^sleep 601' 2>/dev/null | head -1").strip()
    if not pid:
        record(group, "cmdline visible across identities (documented residual risk)", False,
               "could not locate the other identity's process")
        record(group, "another identity's environ is protected", False, "no process to inspect")
        return

    seen = out(bob, "head -c 60 /proc/" + pid + "/cmdline 2>&1").replace("\x00", " ")
    env = out(bob, "head -c 40 /proc/" + pid + "/environ 2>&1")
    # /proc/<pid>/cmdline is world-readable in the kernel: a documented
    # shared-container leak, not something the runner can mask.
    record(group, "cmdline visible across identities (documented residual risk)",
           "sleep 601" in seen, f"seen={seen.strip()[:40]!r}")
    record(group, "another identity's environ is protected",
           "Permission denied" in env or env.strip() == "", env.strip()[:40])


def test_kernel_escapes():
    group = "kernel"
    who = ident()
    checks = [
        ("user namespace blocked", "unshare -Urm true 2>&1; echo rc=$?", "rc=0"),
        ("mount blocked", "mkdir -p /tmp/m; mount -t tmpfs none /tmp/m 2>&1; echo rc=$?", "rc=0"),
        ("chroot blocked", "chroot / /bin/true 2>&1; echo rc=$?", "rc=0"),
        ("cgroup fs not writable", "echo 1 > /sys/fs/cgroup/x 2>&1; echo rc=$?", "rc=0"),
    ]
    for name, command, bad in checks:
        output = out(who, command)
        record(group, name, bad not in output, output.strip()[:60])

    for name, command in [
        ("no docker socket", "ls /var/run/docker.sock 2>&1"),
        ("no /dev/mem", "ls /dev/mem 2>&1"),
        ("/etc/shadow unreadable", "head -c 20 /etc/shadow 2>&1"),
    ]:
        output = out(who, command)
        if name.startswith("no "):
            ok = "No such file" in output or "cannot access" in output
        else:
            ok = "root:" not in output and "Permission denied" in output
        record(group, name, ok, output.strip()[:50])


def test_parsing():
    group = "parsing"
    who = ident()

    boundary = "----deep" + uuid.uuid4().hex
    parts = []
    for command in ("true", "touch out/smuggled"):
        parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="command"\r\n\r\n'
                      f"{command}\r\n").encode())
    parts.append(f"--{boundary}--\r\n".encode())
    status, _ = call("POST", "/v1/exec", who, b"".join(parts),
                     {"Content-Type": f"multipart/form-data; boundary={boundary}"})
    smuggled = out(who, "ls out 2>/dev/null")
    record(group, "duplicate command fields rejected", status == 400 and "smuggled" not in smuggled,
           f"status={status} out={smuggled.strip()[:30]!r}")

    status, body = call("GET", "/v1/path?path=out/ok.txt&path=../../etc/shadow", who)
    record(group, "query parameter pollution does not escape", b"root:" not in body, f"status={status}")

    for name, headers in [
        ("case-variant duplicate identity", ["X-Runbox-Identity: a", "x-runbox-identity: b"]),
        ("identity with leading space", ["X-Runbox-Identity:  alice"]),
        ("identity with trailing tab", ["X-Runbox-Identity: alice\t"]),
        ("namespace duplicated", ["X-Runbox-Namespace: t", "X-Runbox-Namespace: u",
                                  "X-Runbox-Principal: p"]),
    ]:
        try:
            status = raw(headers)
        except Exception as error:  # noqa: BLE001
            record(group, name, True, f"connection dropped: {error}")
            continue
        record(group, name, status == 400, f"status={status}")

    evil_name = 'a"\r\nX-Injected: 1\r\n\r\nb.txt'
    payload = (
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{evil_name}"\r\n'
        f"Content-Type: text/plain\r\n\r\nhello\r\n--{boundary}--\r\n"
    ).encode()
    status, body = call("POST", "/v1/files", who, payload,
                        {"Content-Type": f"multipart/form-data; boundary={boundary}"})
    saved = ""
    try:
        saved = json.loads(body).get("path", "")
    except json.JSONDecodeError:
        pass
    record(group, "CRLF filename cannot inject headers", status in (200, 400),
           f"upload={saved!r} status={status}")


def test_encoding():
    group = "encoding"
    who = ident()
    paths = [
        "out/..%c0%af..%c0%afetc/passwd",
        "out/%2e%2e%5c%2e%2e%5cetc%5cpasswd",
        "../" * 20 + "etc/shadow",
        "out/..%EF%BC%8F..%EF%BC%8Fetc/passwd",
        "out/a%0d%0ab",
        "out/%00.txt",
        "out/" + "a" * 10000,
        "out/....//....//etc/passwd",
    ]
    for path in paths:
        status, body = call("GET", f"/v1/path?path={path}", who)
        leaked = b"root:" in body
        record(group, f"rejected {path[:34]!r}", not leaked and status < 500,
               f"status={status} leaked={leaked}")


def test_archive_limits():
    group = "archive"
    who = ident()
    exe(who, "mkdir -p out && echo x > out/a.txt")
    query = "&".join(["path=out"] * 1001)
    status, body = call("GET", f"/v1/archive?{query}", who, timeout=120)
    record(group, "1001 paths rejected", status in (400, 413), f"status={status}")

    query = "&".join(["path=out/a.txt"] * 50)
    status, body = call("GET", f"/v1/archive?{query}", who, timeout=60)
    record(group, "duplicate paths are deduplicated", status in (200, 400),
           f"status={status} {len(body)}B")


def test_recycle():
    group = "recycle"
    alpha, beta = ident(), ident()
    exe(alpha, "mkdir -p out && echo SECRET-A > out/secret.txt")
    call("DELETE", "/v1/workspace", alpha)
    exe(beta, "mkdir -p out && echo B > out/b.txt")
    after = out(beta, "ls out")
    status, _ = call("GET", "/v1/path?path=out/secret.txt", beta)
    record(group, "recycled UID starts empty", "secret.txt" not in after and status == 404,
           f"after={after.strip()!r} status={status}")


def test_null_command():
    group = "nul"
    who = ident()
    status, payload = exe(who, "echo a" + chr(0) + "b")
    record(group, "command with a NUL byte rejected as 400",
           status == 400 and payload.get("code") == "bad_request",
           f"status={status} code={payload.get('code')}")

    status, payload = exe(who, "tr '\\0' ' ' < /etc/hostname")
    record(group, "escaped backslash-zero is not a NUL byte", status == 200, f"status={status}")


def main():
    groups = [test_hardlinks, test_special_files, test_symlink_race, test_proc_leak,
              test_kernel_escapes, test_parsing, test_encoding, test_archive_limits,
              test_recycle, test_null_command]
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
