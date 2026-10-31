import io
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from runbox import Client, FileUpload, Identity, RunboxError


class NonSeekable:
    """A stream without a length, like a request body being proxied through."""

    def __init__(self, payload: bytes):
        self._buffer = io.BytesIO(payload)

    def read(self, size: int = -1) -> bytes:
        return self._buffer.read(size)


class Handler(BaseHTTPRequestHandler):
    requests = []

    def log_message(self, *_args):
        return

    def _read_body(self) -> bytes:
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            chunks = []
            while True:
                line = self.rfile.readline().strip()
                size = int(line.split(b";")[0], 16)
                if size == 0:
                    self.rfile.readline()
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.read(2)
            return b"".join(chunks)
        length = int(self.headers.get("Content-Length", "0"))
        return self.rfile.read(length)

    def _record(self):
        body = self._read_body()
        Handler.requests.append(
            {
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }
        )
        return body

    def do_POST(self):
        body = self._record()
        if self.path == "/v1/exec":
            if self.headers.get("Content-Type", "").startswith("multipart/"):
                self._json(200, {"exit_code": 0, "stdout": "multipart", "files": []})
                return
            payload = json.loads(body)
            self._json(200, {"exit_code": 0, "stdout": payload["command"], "files": []})
            return
        if self.path in ("/v1/files", "/v1/files/batch"):
            self._json(
                200,
                {
                    "files": [
                        {"path": "files/a.txt", "name": "a.txt", "size": 3},
                        {"path": "files/b.txt", "name": "b.txt", "size": 3},
                    ]
                },
            )
            return
        self._json(404, {"code": "not_found", "message": "not found"})

    def do_GET(self):
        self._record()
        if self.path.startswith("/v1/path"):
            if "missing" in self.path:
                self._json(404, {"code": "not_found", "message": "not found"})
                return
            self._bytes(200, b"path-data")
            return
        if self.path.startswith("/v1/archive"):
            self._bytes(200, b"tar-data")
            return
        if self.path.startswith("/v1/list"):
            self._json(200, {"entries": [{"name": "out", "is_dir": True}]})
            return
        if self.path == "/health":
            self._json(200, {"status": "healthy"})
            return
        if self.path == "/ready":
            self._json(200, {"status": "ready"})
            return
        if self.path == "/metrics":
            self._bytes(200, b"runbox_uptime_seconds 1\n")
            return
        self._json(404, {"code": "not_found", "message": "not found"})

    def do_DELETE(self):
        self._record()
        self._json(200, {"deleted": True})

    def _json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _bytes(self, status, body):
        self.send_response(status)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        host, port = cls.server.server_address
        cls.client = Client(
            f"http://{host}:{port}",
            api_key="test-key",
            identity=Identity(namespace="team-a", principal="user-42"),
        )

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def setUp(self):
        Handler.requests.clear()

    def test_exec_sends_auth_and_identity(self):
        result = self.client.exec("echo hello")
        self.assertEqual(result["stdout"], "echo hello")
        request = Handler.requests[0]
        self.assertEqual(request["headers"]["Authorization"], "Bearer test-key")
        self.assertEqual(request["headers"]["X-Runbox-Namespace"], "team-a")
        self.assertEqual(request["headers"]["X-Runbox-Principal"], "user-42")

    def test_batch_upload_streams_files(self):
        with tempfile.TemporaryDirectory() as directory:
            first = Path(directory, "a.txt")
            second = Path(directory, "b.txt")
            first.write_bytes(b"aaa")
            second.write_bytes(b"bbb")
            uploaded = self.client.upload_batch(
                [FileUpload("a.txt", path=first), FileUpload("b.txt", path=second)]
            )
        self.assertEqual([item["path"] for item in uploaded], ["files/a.txt", "files/b.txt"])
        request = Handler.requests[0]
        self.assertIn(b"aaa", request["body"])
        self.assertIn(b"bbb", request["body"])
        self.assertEqual(int(request["headers"]["Content-Length"]), len(request["body"]))

    def test_upload_stream_without_length_is_chunked(self):
        stream = NonSeekable(b"forwarded-bytes")
        uploaded = self.client.upload_stream("report.csv", stream, content_type="text/csv")
        self.assertEqual(uploaded["path"], "files/a.txt")
        request = Handler.requests[0]
        self.assertEqual(request["headers"].get("Transfer-Encoding"), "chunked")
        self.assertNotIn("Content-Length", request["headers"])
        self.assertIn(b"forwarded-bytes", request["body"])
        self.assertIn(b'filename="report.csv"', request["body"])
        self.assertIn(b"Content-Type: text/csv", request["body"])

    def test_upload_stream_with_size_sends_content_length(self):
        stream = io.BytesIO(b"exact-bytes")
        self.client.upload_stream("exact.bin", stream)
        request = Handler.requests[0]
        self.assertEqual(int(request["headers"]["Content-Length"]), len(request["body"]))
        self.assertIn(b"exact-bytes", request["body"])

    def test_exec_forwards_attachment_stream(self):
        result = self.client.exec(
            "cat files/input.txt",
            files=[FileUpload("input.txt", stream=NonSeekable(b"stream-body"))],
        )
        self.assertEqual(result["stdout"], "multipart")
        request = Handler.requests[0]
        self.assertIn(b"stream-body", request["body"])
        self.assertIn(b'name="command"', request["body"])

    def test_download_path_and_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            path_file = Path(directory, "path.bin")
            archive_file = Path(directory, "archive.tar")
            self.client.download_path("out/report.txt", path_file)
            self.client.download_archive(["out", "tmp"], archive_file)
            self.assertEqual(path_file.read_bytes(), b"path-data")
            self.assertEqual(archive_file.read_bytes(), b"tar-data")

    def test_download_into_open_stream(self):
        sink = io.BytesIO()
        self.client.download_path_to("out/report.txt", sink)
        self.assertEqual(sink.getvalue(), b"path-data")
        self.assertFalse(sink.closed)

    def test_open_path_streams_body(self):
        response = self.client.open_path("out/report.txt")
        with response:
            self.assertEqual(response.read(), b"path-data")

    def test_iter_path_yields_chunks_and_forwards_range(self):
        chunks = list(self.client.iter_path("out/report.txt", chunk_size=4, range_header="bytes=0-3"))
        self.assertEqual(b"".join(chunks), b"path-data")
        self.assertTrue(all(len(chunk) <= 4 for chunk in chunks))
        self.assertEqual(Handler.requests[0]["headers"]["Range"], "bytes=0-3")

    def test_iter_archive_yields_chunks(self):
        self.assertEqual(b"".join(self.client.iter_archive(["out"])), b"tar-data")

    def test_metrics_and_wait_ready(self):
        self.assertIn("runbox_uptime_seconds", self.client.metrics())
        self.assertEqual(self.client.wait_ready(timeout=5)["status"], "ready")

    def test_server_error_is_typed(self):
        with self.assertRaises(RunboxError) as caught:
            self.client.download_path("missing", Path(tempfile.gettempdir(), "missing.bin"))
        self.assertEqual(caught.exception.status, 404)
        self.assertEqual(caught.exception.code, "not_found")


if __name__ == "__main__":
    unittest.main()
