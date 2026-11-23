"""Optional, pinned local encoder for the isolated enterprise smoke database."""
import json
import os
from pathlib import Path
import subprocess
import time
from uuid import UUID


def start_encoder(env, root, processes, log, port):
    env.pop("RETRIEVAL_URL", None)
    python = os.environ.get("SMOKE_RETRIEVAL_PYTHON")
    if not python:
        return None
    env["RETRIEVAL_URL"] = f"http://127.0.0.1:{port}"
    command = [python, str(root / "retrieval/server.py")]
    service_env = {"CONNECTOR_TOKEN": env["CONNECTOR_TOKEN"],
                   "RETRIEVAL_MODEL_DIR": os.environ["RETRIEVAL_MODEL_DIR"],
                   "RETRIEVAL_PORT": str(port)}

    def start():
        proc = subprocess.Popen(command, env=service_env, stdout=log, stderr=log)
        processes.append(proc)
        return proc
    return start(), start


def verify_agent_semantics(service, env, frontend, port, evidence, worker_log, pg, database, restart_worker):
    """Keep indexed originals while comparing availability in the same database."""
    proc, restart = service
    command = ["node", str(frontend / "tests/agent-semantic-lookup.mjs")]
    case_env = {**env, "QUICK_ENDPOINT": f"https://localhost:{port}/v1", "VISUAL_EVIDENCE": str(evidence)}
    subprocess.run(command + ["online"], env=case_env, check=True, timeout=125)
    proc.terminate()
    proc.wait(timeout=10)
    subprocess.run(command + ["offline"], env=case_env, check=True, timeout=75)
    offset = worker_log.stat().st_size
    subprocess.run(command + ["prepare-fresh"], env=case_env, check=True, timeout=35)
    # Drive execution during the actual background indexer's outage backoff,
    # with the encoder restored and the new attachments still unindexed.
    with worker_log.open(encoding="utf-8", errors="replace") as log:
        log.seek(offset)
        deadline = time.monotonic() + 19
        while time.monotonic() < deadline:
            if "资料向量索引暂不可用，将重试" in log.read():
                break
            time.sleep(0.1)
        else:
            raise AssertionError("Background indexer did not enter outage backoff")
    recovered = restart()
    subprocess.run(command + ["fresh"], env=case_env, check=True, timeout=65)
    report_path = evidence / "agent-semantic-lookup.json"
    report = json.loads(report_path.read_text())
    for case, expected in (("freshProject", 2), ("service", 2), ("freshApi", 2), ("backlog", 1)):
        row = report[case]
        run = UUID(row["run"])
        ledger = json.loads(subprocess.check_output(pg + ["psql", "-At", "-U", "alliance", "-d", database, "-c", f"""
          select jsonb_build_object('charges',(select count(*) from spend where run_id=r.id),
            'attempts',(select count(*) from call_attempts where run_id=r.id),
            'freshCitation',exists(select from trace_events where run_id=r.id and code='lookup_semantic_fresh'))
          from runs r where r.id='{run}'"""], text=True))
        assert ledger["charges"] == ledger["attempts"] == expected, (case, ledger)
        assert ledger["freshCitation"] == (case != "backlog"), (case, ledger)
        row["ledger"] = ledger
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    restart_worker()
    subprocess.run(command + ["resume-backlog"], env=case_env, check=True, timeout=60)
    resumed = json.loads(report_path.read_text())
    run = UUID(resumed["resumedBacklog"]["run"])
    ledger = json.loads(subprocess.check_output(pg + ["psql", "-At", "-U", "alliance", "-d", database, "-c", f"""
      select jsonb_build_object('charges',(select count(*) from spend where run_id=r.id),
        'attempts',(select count(*) from call_attempts where run_id=r.id),
        'completedRechecks',(select count(*) from trace_events where run_id=r.id and code='lookup_index_rechecked'),
        'citationsOnly',(select bool_and(c ?& array['Source','Version','Start','End','Hash'] and
          (c - array['Source','Version','Start','End','Hash'])='{{}}'::jsonb)
          from lookup_index_waits w cross join lateral jsonb_array_elements(w.observation::jsonb) c where w.run_id=r.id))
      from runs r where r.id='{run}'"""], text=True))
    assert ledger == {"charges": 2, "attempts": 2, "completedRechecks": 1, "citationsOnly": True}, ledger
    resumed["resumedBacklog"]["ledger"] = ledger
    report_path.write_text(json.dumps(resumed, ensure_ascii=False, indent=2) + "\n")
    subprocess.run(["python3", str(Path(__file__).with_name("smoke_semantic_lookup_files.py")), str(evidence), "--fresh"], check=True)
    return recovered, restart


def verify_encoder_outage(req, service, evidence: Path):
    if not service:
        return
    proc, restart = service
    proc.terminate()
    proc.wait(timeout=10)
    project = "projects/" + req("projects", {"goal":"离线期间的资料"})["id"]
    def upload(text):
        return req(project + "/sources?name=note.txt", text.encode())["id"]

    kept, removed = upload("恢复索引应当保留这个片段。"), upload("已删除的干扰片段。")
    req(project + "/sources/" + removed, method="DELETE")
    query = project + "/retrieval?q=" + "note"
    # A fresh query still works when the semantic service is down.
    result = req(project + "/retrieval?q=" + "%E7%89%87%E6%AE%B5")
    assert result["mode"] == "unavailable"
    assert [h["id"] for h in result["result"]["hits"]] == [kept]
    restart()
    for _ in range(100):
        result = req(query)
        if result["mode"] == "hybrid" and result["result"]["indexed"] == 1:
            break
        time.sleep(0.3)
    else:
        raise AssertionError("Recovered worker failed to index the outstanding source")
    assert result["result"]["passages"] == 1
    assert all(h["id"] == kept for h in req(project + "/retrieval?q=%E7%89%87%E6%AE%B5")["result"]["hits"])
    (evidence/"retrieval-outage.json").write_text(json.dumps({
        "encoderStopped":True,"exactSearchDuringOutage":True,"remainingPassages":1,
        "indexedAfterRestart":1,"revokedPassagesAfterRestart":0,
    },indent=2))


def assess_new_cases(req, evidence, semantic):
    """Separate transfer cases, frozen before evaluating the new domains."""
    from urllib.parse import quote
    project = "projects/" + req("projects", {"goal":"现场活动与客户支持资料核对"})["id"]
    passages = [
        "签到设备发生故障时，现场负责人启用离线纸质名册登记。",
        "活动结束后由场务清点借出的展架，并在次日上午归还。",
        "对于重复扣款，客服先索取交易流水号，再交结算专员核实。",
        "客户变更收货地址需在发货前提交工单，客服在工单中记录新地址。",
    ]
    for file, pair in [("event.txt", passages[:2]), ("support.txt", passages[2:])]:
        req(project + "/sources?name=" + file, ("📌\n" + "\n".join(pair)).encode())
    cases = [
        ("活动现场没法用机器签到，要怎么登记？", 0),
        ("展架最晚什么时候还？", 1),
        ("重复扣款", 2),
        ("货还没发，换个收件地点该怎么办？", 3),
        ("地铁公交费报销需要哪种票据？", None),
        ("场地最多可以入场几人？", None),
    ]
    def search(query):
        return req(project + "/retrieval?q=" + quote(query))
    for _ in range(80):
        r = search("签到")
        if not semantic or (r["mode"] == "hybrid" and r["result"]["indexed"] == r["result"]["passages"]):
            break
        time.sleep(.25)
    else:
        raise AssertionError("Transfer corpus was not fully indexed")
    results = []
    for query, ordinal in cases:
        hits = search(query)["result"]["hits"]
        expected = None if ordinal is None else passages[ordinal].removesuffix("。")
        actual = hits[0]["excerpt"] if hits else None
        results.append({"query":query, "expected":expected, "actual":actual, "correct":expected == actual, "hits":hits})
    (evidence/"retrieval-transfer.json").write_text(json.dumps({
        "mode": r["mode"], "firstOrEmptyCorrect":sum(row["correct"] for row in results),
        "total":len(results), "cases":results,
    }, ensure_ascii=False, indent=2))
