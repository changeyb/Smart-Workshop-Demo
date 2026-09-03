"""隔离回归测试：仅启动临时回环 HTTP 服务，不连接业务服务或写数据库。"""

import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import unittest
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


SCRIPT = Path(__file__).with_name("simulate.sh")


class SimulateTests(unittest.TestCase):
    def run_script(self, host):
        return subprocess.run(
            ["bash", str(SCRIPT), host],
            env={**os.environ, "TZ": "America/New_York", "WS_TOKEN": "test-token",
                 "NO_PROXY": "127.0.0.1", "no_proxy": "127.0.0.1"},
            capture_output=True, text=True, timeout=15,
        )

    def run_mock(self, failure=None):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def respond(self):
                raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                payload = json.loads(raw) if raw else None
                requests.append((self.command, self.path, payload,
                                 self.headers.get("Authorization")))
                data = {}
                if self.path == "/api/v1/events":
                    invalid = payload[0]["event_id"] == "sim-bad01"
                    duplicate = len(requests) == 3
                    data = {"accepted": 0 if invalid or duplicate else len(payload),
                            "duplicated": 1 if duplicate else 0,
                            "rejected": [{"event_id": "sim-bad01",
                                          "reason": "missing spot.status"}] if invalid else []}
                elif self.path == "/api/v1/dashboard":
                    data = {"stats": {"people": 3}}
                status, body = 200, json.dumps({"code": 0, "message": "ok", "data": data})
                if failure and len(requests) == failure[0]:
                    _, status, body = failure
                if status is None:
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.close_connection = True
                    return
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body.encode())

            do_POST = respond
            do_GET = respond

        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            try:
                result = self.run_script(f"http://127.0.0.1:{server.server_port}/")
            finally:
                server.shutdown()
                worker.join()
        return result, requests

    def assert_stopped(self, result, message):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(message, result.stderr)
        self.assertNotIn("完成。", result.stdout)
        self.assertNotIn("Traceback", result.stderr)

    def test_success_and_exact_timestamps(self):
        before = datetime.now(timezone.utc)
        result, requests = self.run_mock()
        after = datetime.now(timezone.utc)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertEqual([r[:2] for r in requests], [
            ("POST", "/api/v1/heartbeat"), ("POST", "/api/v1/events"),
            ("POST", "/api/v1/events"), ("POST", "/api/v1/events"),
            ("POST", "/api/v1/spots/A-05/override"), ("GET", "/api/v1/dashboard"),
        ])
        # 即使环境残留 WS_TOKEN，模拟请求也不再发送鉴权头。
        self.assertTrue(all(r[3] is None for r in requests))
        now = datetime.fromisoformat(requests[0][2]["time"])
        self.assertLessEqual(before - timedelta(milliseconds=1), now)
        self.assertLessEqual(now, after)
        offsets = [180, 180, 120, 12, 11, 10, 40, 34, 34, 150, 150, 4, 30, 300, 180]
        batch = requests[1][2]
        self.assertEqual(len(batch), len(offsets))
        for event, minutes in zip(batch, offsets):
            timestamp = datetime.fromisoformat(event["occur_time"])
            self.assertEqual(timestamp.utcoffset(), timedelta(hours=8))
            self.assertEqual(now - timestamp, timedelta(minutes=minutes))
        self.assertEqual(requests[2][2][0], batch[0])
        self.assertEqual(requests[3][2][0]["occur_time"], requests[0][2]["time"])
        self.assertIn('"people": 3', result.stdout)
        self.assertIn("missing spot.status", result.stdout)
        self.assertIn("完成。", result.stdout)

    def test_http_errors_stop_on_first_request(self):
        for status in (301, 401, 500):
            with self.subTest(status=status):
                result, requests = self.run_mock((1, status, "request failed"))
                self.assert_stopped(result, f"HTTP {status}")
                self.assertIn("POST http://127.0.0.1:", result.stderr)
                self.assertEqual(len(requests), 1)

    def test_business_failure_with_http_200(self):
        result, requests = self.run_mock((2, 200, '{"code":50000,"message":"db error"}'))
        self.assert_stopped(result, "code=50000")
        self.assertIn("db error", result.stderr)
        self.assertEqual(len(requests), 2)

    def test_empty_and_invalid_json(self):
        for body in ("", "<html>Not JSON</html>"):
            with self.subTest(body=body):
                result, requests = self.run_mock((1, 200, body))
                self.assert_stopped(result, "空响应或无效 JSON")
                self.assertEqual(len(requests), 1)

    def test_missing_business_code(self):
        result, requests = self.run_mock((1, 200, '{}'))
        self.assert_stopped(result, "业务状态码 code")
        self.assertEqual(len(requests), 1)

    def test_missing_dashboard_stats(self):
        result, requests = self.run_mock((6, 200, '{"code":0,"data":{}}'))
        self.assert_stopped(result, "data.stats")
        self.assertEqual(len(requests), 6)

    def test_connection_failure(self):
        # 服务端收下请求后断开连接，不返回 HTTP 响应。
        result, requests = self.run_mock((1, None, ""))
        self.assert_stopped(result, "curl 退出码 52")
        self.assertEqual(len(requests), 1)
        self.assertNotIn("==> 2.", result.stdout)


if __name__ == "__main__":
    unittest.main()
