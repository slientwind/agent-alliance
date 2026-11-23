"""Optional open-weight evidence. Run on loopback, separately from synthetic provider tests."""

import hashlib
import json
import os
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

from smoke_tool_choice_files import verify_tool_choice_files
from smoke_column_suggestions import verify_column_suggestions
from smoke_lookup_receipt_files import verify_lookup_receipts
from smoke_lookup_files import verify_lookup_files
from smoke_calculation_files import verify_calculation_files
from smoke_receipt_files import verify_receipt_files
from smoke_planning_files import verify_planning_files
from smoke_semantic_lookup_files import verify as verify_semantic_lookup


MODEL_ALIAS = "local-course-qwen"


def relay_course_model(handler, request):
    """Relay the original model request, never substitute prepared slide text."""
    port = getattr(handler.server, "course_model_port", None)
    if not port:
        handler.send_error(503, "Local course model is offline")
        return
    data = json.dumps(request, ensure_ascii=False).encode()
    call = urllib.request.Request(
        f"http://127.0.0.1:{port}/v1/chat/completions",
        data,
        {"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(call, timeout=85) as response:
            raw = response.read()
        trace = getattr(handler.server, "planning_evaluation_log", None)
        if trace and "本次资料概况：" in request["messages"][-1]["content"]:
            # Only this isolated synthetic provider receives the test requests.
            reply = json.loads(raw)["choices"][0]["message"]["content"]
            with trace.open("a") as stream:
                stream.write(json.dumps({"input":request["messages"][-1]["content"],"reply":reply},ensure_ascii=False)+"\n")
        route_log = getattr(handler.server, "tool_choice_evaluation_log", None)
        if route_log and "已获授权的报表汇总工具" in request["messages"][0]["content"]:
            reply = json.loads(raw)["choices"][0]["message"]["content"]
            with route_log.open("a") as stream:
                stream.write(json.dumps({"input":request["messages"][1]["content"],"reply":reply},ensure_ascii=False)+"\n")
        lookup_log = getattr(handler.server, "lookup_receipt_evaluation_log", None)
        user_input = request["messages"][1]["content"]
        if lookup_log and "设备借用规程" in user_input and "核对出库单" in user_input:
            assert "PRIVATE-LOOKUP-ROW" not in str(request["messages"])
            assert "培训周三十四点" not in str(request["messages"])
            reply = json.loads(raw)["choices"][0]["message"]["content"]
            with lookup_log.open("a") as stream:
                stream.write(json.dumps({"messages":request["messages"][1:],"reply":reply},ensure_ascii=False)+"\n")
        column_log = getattr(handler.server, "column_suggestion_evaluation_log", None)
        if column_log and "你为业务人员推荐原表列" in request["messages"][0]["content"]:
            assert "PRIVATE-ROW-367" not in user_input and "敏感东区.csv" not in user_input
            reply = json.loads(raw)["choices"][0]["message"]["content"]
            with column_log.open("a") as stream:
                stream.write(json.dumps({"headers": json.loads(user_input), "reply": reply}, ensure_ascii=False)+"\n")
        handler.send_response(200)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(raw)))
        handler.end_headers()
        handler.wfile.write(raw)
    except urllib.error.HTTPError as error:
        handler.send_error(error.code)
    except OSError:
        handler.send_error(503)


def verify_course_model(provider, env, frontend, out, free_port, logs, tool_choice_only=False, column_python=None):
    binary = Path(os.environ["COURSE_LLAMA_SERVER"]).resolve(strict=True)
    model = Path(os.environ["COURSE_MODEL_FILE"]).resolve(strict=True)
    port = free_port()
    command = [
        str(binary),
        "-m",
        str(model),
        "--host",
        "127.0.0.1",
        "--port",
        str(port),
        "--alias",
        MODEL_ALIAS,
        "--ctx-size",
        "8192",
        "--parallel",
        "1",
        "--temp",
        "0",
        "--seed",
        "41",
        "--reasoning",
        "off",
        "--reasoning-budget",
        "0",
    ]
    with model.open("rb") as source:
        digest = hashlib.sha256()
        for block in iter(lambda: source.read(4 << 20), b""):
            digest.update(block)
    (out / "course-local-runtime.json").write_text(
        json.dumps(
            {
                "provider": "llama-server on loopback, HTTPS bridge",
                "file": model.name,
                "modelSHA256": digest.hexdigest(),
                "modelAlias": MODEL_ALIAS,
                "context": 8192,
                "temperature": 0,
                "seed": 41,
                "reasoning": "off",
                "serverVersion": subprocess.check_output(
                    [str(binary), "--version"], stderr=subprocess.STDOUT, text=True
                )[-950:],
            },
            indent=2,
        )
        + "\n"
    )
    proc = subprocess.Popen(command, stdout=logs, stderr=logs)
    try:
        deadline = time.monotonic() + 70
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise RuntimeError("local course model exited before readiness")
            try:
                with urllib.request.urlopen(
                    f"http://127.0.0.1:{port}/health", timeout=2
                ) as response:
                    if response.status == 200:
                        break
            except OSError:
                time.sleep(0.25)
        else:
            raise RuntimeError("local course model did not become ready")
        provider.planning_evaluation_log = out / "planning-model-replies.jsonl"
        provider.tool_choice_evaluation_log = out / "tool-choice-local-model-replies.jsonl"
        provider.course_model_port = port
        provider.lookup_receipt_evaluation_log = out / "lookup-receipt-local-model-replies.jsonl"
        subprocess.run(["node",str(frontend/"tests/tool-choice.mjs")],check=True,timeout=170,
                       env={**env,"QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1","VISUAL_EVIDENCE":str(out),"TOOL_CHOICE_REAL":"1"})
        verify_tool_choice_files(out,"local")
        if tool_choice_only:
            return
        if column_python is not None:
            provider.column_suggestion_evaluation_log = out / "column-suggestion-local-reply.jsonl"
            verify_column_suggestions(frontend, env, out, column_python, f"https://localhost:{provider.server_port}/v1", local=True)
        subprocess.run(["node",str(frontend/"tests/lookup-receipts.mjs")], check=True, timeout=155,
                       env={**env,"QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1",
                            "VISUAL_EVIDENCE":str(out),"LOOKUP_RECEIPT_REAL":"1"})
        verify_lookup_receipts(out,"local")
        if env.get("RETRIEVAL_URL"):
            subprocess.run(["node", str(frontend / "tests/agent-semantic-lookup.mjs"), "local"],
                           check=True, timeout=125, env={**env, "QUICK_ENDPOINT": f"https://localhost:{provider.server_port}/v1",
                                                       "VISUAL_EVIDENCE": str(out)})
            verify_semantic_lookup(out, local=True)
        subprocess.run(["node",str(frontend/"tests/draft-revision.mjs")],check=True,timeout=150,
                       env={**env,"QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1","VISUAL_EVIDENCE":str(out),"DRAFT_REAL_MODEL":"1"})
        subprocess.run(
            ["node", str(frontend / "tests/course-model.mjs")],
            check=True,
            timeout=360,
            env={
                **env,
                "QUICK_ENDPOINT": f"https://localhost:{provider.server_port}/v1",
                "VISUAL_EVIDENCE": str(out),
            },
        )
        subprocess.run(["node",str(frontend/"tests/agent-source-lookup.mjs")],check=True,timeout=210,
                       env={**env,"QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1","VISUAL_EVIDENCE":str(out),"LOOKUP_COMPARE":os.environ.get("LOOKUP_COMPARE",""),"LOOKUP_REAL_MODEL":"1"})
        verify_lookup_files(out,"local-model")
        verify_calculation_files(out,True)
        verify_receipt_files(out,True)
        cases=json.loads((frontend / "tests/planning-cases.json").read_text())
        for scenario, sample in cases.items():
            subprocess.run(["node",str(frontend/"tests/planning-inputs.mjs")],check=True,timeout=250,
                           env={**env,"QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1","VISUAL_EVIDENCE":str(out),
                                "PLANNING_REAL_MODEL":"1","PLANNING_CASE":scenario})
            mode="local" if scenario=="handoff" else "local-"+scenario
            verify_planning_files(out,mode,sample)
        subprocess.run(["node", str(frontend/"tests/tool-gap-model.mjs")], check=True, timeout=170,
                       env={**env, "QUICK_ENDPOINT":f"https://localhost:{provider.server_port}/v1","VISUAL_EVIDENCE":str(out)})
    finally:
        provider.planning_evaluation_log = None
        provider.tool_choice_evaluation_log = None
        provider.course_model_port = None
        provider.lookup_receipt_evaluation_log = None
        provider.column_suggestion_evaluation_log = None
        proc.terminate()
        try:
            proc.wait(timeout=12)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
