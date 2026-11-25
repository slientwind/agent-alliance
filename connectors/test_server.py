import http.client
import json
import os
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch

with patch.dict(os.environ, {"CONNECTOR_TOKEN": "unit-test"}):
    import server


class ConcurrentConnectorTest(unittest.TestCase):
    def test_blocked_invocation_does_not_hold_up_other_requests(self):
        entered = threading.Event()
        release = threading.Event()
        execute = server.execute

        def hold_one_invocation(payload):
            if payload["text"] == "first":
                entered.set()
                if not release.wait(5):
                    raise TimeoutError("test did not release the first invocation")
            return execute(payload)

        listener = server.create_server("127.0.0.1", 0)
        self.addCleanup(listener.server_close)
        serving = threading.Thread(target=listener.serve_forever)
        serving.start()
        self.addCleanup(serving.join, 3)
        self.addCleanup(listener.shutdown)

        def invoke(text):
            conn = http.client.HTTPConnection(*listener.server_address, timeout=2)
            try:
                conn.request(
                    "POST", "/invoke", json.dumps({"operation": "extract", "text": text}),
                    {"Authorization": "Bearer unit-test"},
                )
                response = conn.getresponse()
                return response.status, json.loads(response.read())
            finally:
                conn.close()

        with patch.object(server, "execute", hold_one_invocation), ThreadPoolExecutor(max_workers=1) as jobs:
            first = jobs.submit(invoke, "first")
            try:
                self.assertTrue(entered.wait(2))
                self.assertEqual(invoke("second"), (200, {"text": "second"}))
                self.assertFalse(first.done())
            finally:
                release.set()
            self.assertEqual(first.result(timeout=2), (200, {"text": "first"}))
