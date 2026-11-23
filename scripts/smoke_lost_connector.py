"""Private recovery drill fixture: finish one calculation, then drop its response."""

import base64
import hashlib
import json
import os
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "connectors"))
from server import Handler, HTTPServer


class RecoveryHandler(Handler):
    def reply(self, status, value):
        if status == 200 and "DRILL_RECEIPT_LOSS_086 · 1.10" in value.get("text", ""):
            data = base64.b64decode(value["file"]["data"], validate=True)
            # Count all actual executions, including any unintended replay after restore.
            with Path(os.environ["DRILL_RECEIPTS"]).open("a") as receipts:
                print(
                    json.dumps(
                        {"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)}
                    ),
                    file=receipts,
                )
            self.close_connection = True
            return
        super().reply(status, value)


if __name__ == "__main__":
    HTTPServer(
        ("127.0.0.1", int(os.environ["CONNECTOR_PORT"])), RecoveryHandler
    ).serve_forever()
