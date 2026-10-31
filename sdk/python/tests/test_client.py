import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from runbox import Client, FileUpload, Identity, RunboxError


class Handler(BaseHTTPRequestHandler):
    requests = []

    def log_message(self, *_args):
        return

    def _record(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
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
            payload = json.loads(body)
            self._json(200, {"exit_code": 0, "stdout": payload["command"], "files": []})
            return
        if self.path == "/v1/files/batch":
            self._json(
                200,
                {
                    "files": [
                        {"file_id": "rbx_a", "name": "a.txt", "size": 3},
                        {"file_id": "rbx_b", "name": "b.txt", "size": 3},
                    ]
                },
            )
            return
        if self.path.startswith("/v1/files/") and self.path.endswith("/keep"):
            self._json(200, {"name": "a.txt", "file_id": "rbx_keep", "size": 3})
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
        self.assertEqual([item["file_id"] for item in uploaded], ["rbx_a", "rbx_b"])
        request = Handler.requests[0]
        self.assertIn(b"aaa", request["body"])
        self.assertIn(b"bbb", request["body"])
        self.assertEqual(int(request["headers"]["Content-Length"]), len(request["body"]))

    def test_download_path_and_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            path_file = Path(directory, "path.bin")
            archive_file = Path(directory, "archive.tar")
            self.client.download_path("out/report.txt", path_file)
            self.client.download_archive(["out", "tmp"], archive_file)
            self.assertEqual(path_file.read_bytes(), b"path-data")
            self.assertEqual(archive_file.read_bytes(), b"tar-data")

    def test_server_error_is_typed(self):
        with self.assertRaises(RunboxError) as caught:
            self.client.download_path("missing", Path(tempfile.gettempdir(), "missing.bin"))
        self.assertEqual(caught.exception.status, 404)
        self.assertEqual(caught.exception.code, "not_found")


if __name__ == "__main__":
    unittest.main()
