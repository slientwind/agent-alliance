"""Exercise the OTLP wire format with the actual receiver in a disposable container."""

import subprocess
import time
from uuid import UUID


class Collector:
    def __init__(self, folder, name, port):
        self.name = name + "-otel"
        self.port = port
        self.config = folder / "collector.yaml"
        self.config.write_text(
            """receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
exporters:
  debug:
    verbosity: detailed
    sampling_initial: 1000
    sampling_thereafter: 1
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [debug]
"""
        )
        self.created = False

    def start(self):
        subprocess.run(
            [
                "docker",
                "create",
                "--name",
                self.name,
                "-p",
                f"127.0.0.1:{self.port}:4318",
                "--mount",
                f"type=bind,src={self.config},dst=/etc/otelcol/config.yaml,readonly",
                "otel/opentelemetry-collector-contrib:0.139.0",
                "--config=/etc/otelcol/config.yaml",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
        )
        self.created = True
        subprocess.run(
            ["docker", "start", self.name], check=True, stdout=subprocess.DEVNULL
        )

    def verify(self, sql, run):
        UUID(run)
        query = (
            f"select count(*) from telemetry_outbox where run_id='{run}' and attempts>0"
        )
        for _ in range(30):
            if int(sql(query)):
                break
            time.sleep(0.2)
        else:
            raise AssertionError(
                "Exporter did not attempt delivery while the receiver was unavailable"
            )
        self.start()
        for _ in range(100):
            if (
                sql(
                    f"select count(*) from telemetry_outbox where run_id='{run}' and outcome='delivered'"
                )
                == "3"
            ):
                break
            time.sleep(0.3)
        else:
            raise AssertionError(
                "Receiver did not acknowledge the root and both call spans"
            )
        log = subprocess.check_output(
            ["docker", "logs", self.name], stderr=subprocess.STDOUT, text=True
        )
        for expected in [
            run.replace("-", ""),
            "alliance-worker",
            "agent.run",
            "agent.invoke",
            "alliance.agent.revision",
            "alliance.state",
        ]:
            assert expected in log, expected
        for excluded in [
            "source72",
            "RECEIPT remote OK",
            "Write and check",
            "local-fixture",
            "Provide receipt",
        ]:
            assert excluded not in log, excluded
        assert (
            int(sql(f"select max(attempts) from telemetry_outbox where run_id='{run}'"))
            >= 2
        )
        assert sql(f"select count(*) from spend where run_id='{run}'") == "2"

    def verify_distinct_attempts(self, receipt):
        first, replacement = receipt["steps"][1]
        assert first["spanId"] != replacement["spanId"]
        for _ in range(50):
            log = subprocess.check_output(["docker","logs",self.name], stderr=subprocess.STDOUT, text=True)
            if all(span["spanId"] in log for span in (first, replacement)):
                return
            time.sleep(0.3)
        raise AssertionError("Collector did not receive distinct original and replacement invocation spans")

    def close(self):
        if self.created:
            subprocess.run(
                ["docker", "rm", "-f", self.name], stdout=subprocess.DEVNULL, check=True
            )
