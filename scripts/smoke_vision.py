"""Use generated images and an exact-byte HTTPS fixture to verify vision calls."""

import base64
import hashlib
import io
import json
import subprocess
from pathlib import Path
from uuid import uuid4
from xml.etree import ElementTree
from zipfile import ZipFile


class VisionFixture:
    def __init__(self, python, directory):
        directory.mkdir()
        subprocess.run([str(python), "-c", """
from PIL import Image
from pathlib import Path
import sys

root = Path(sys.argv[1])
Image.new('RGB', (48, 32), '#1850dd').save(root / 'board.png')
Image.new('RGB', (48, 32), '#ce271e').save(root / 'replacement.png')
Image.new('RGB', (48, 32), '#14a45a').save(root / 'board.jpg')
""", str(directory)], check=True)
        self.directory = directory
        self.originals = {
            self.digest((directory / name).read_bytes()): (media, description)
            for name, media, description in (
                ("board.png", "image/png", "蓝色标签"),
                ("replacement.png", "image/png", "红色标签"),
                ("board.jpg", "image/jpeg", "绿色标签"),
            )
        }
        self.receipts = []

    @staticmethod
    def digest(data):
        return hashlib.sha256(data).hexdigest()

    def response(self, request):
        messages = request["messages"]
        if request["model"] == "vision-planner-fixture":
            assert all(isinstance(message["content"], str) for message in messages)
            user = messages[1]["content"]
            assert '"pictures":1' in user and "board.png" not in user
            assert "data:image/" not in user
            catalog = json.loads(user.split("候选能力：", 1)[1].split("\n业务对话：", 1)[0])
            agent = next(item for item in catalog if item["operation"] == "vision")
            answer = json.dumps({
                "kind": "plan", "text": "核对图片中的标签颜色 VISION_PLANNING",
                "agents": [agent["id"]], "reasons": ["读取固定版本的图片"], "missing": [],
            }, ensure_ascii=False)
            self.receipts.append({"purpose": "planning", "pictureCount": 1, "payloads": []})
        else:
            content = next(message["content"] for message in messages if message["role"] == "user")
            assert isinstance(content, list)
            prompt = content[0]["text"]
            pictures = []
            for i, item in enumerate(content):
                if item["type"] != "image_url":
                    continue
                info, encoded = item["image_url"]["url"].split(";base64,", 1)
                original = base64.b64decode(encoded, validate=True)
                digest = self.digest(original)
                media, text = self.originals[digest]
                assert info == "data:" + media
                label = content[i-1]["text"]
                assert label.startswith("图片 ")
                pictures.append({"label": label, "sha256": digest, "media": media, "result": text})
            assert pictures
            answer = "，".join(picture["result"] for picture in pictures)
            purpose = next(marker for marker in ("VISION_REVISION", "VISION_TRIAL", "VISION_DIRECT", "VISION_PLANNING", "VISION_COMPOSE", "VISION_WEB", "VISION_API") if marker in prompt)
            self.receipts.append({"purpose": purpose, "payloads": pictures})
        return {"choices": [{"message": {"content": answer}}], "usage": {"total_tokens": 11}}

    def verify(self, frontend, env, report, endpoint):
        subprocess.run(["node", str(frontend / "tests/vision.mjs")],
                       env={**env, "VISION_SAMPLES": str(self.directory),
                            "VISUAL_EVIDENCE": str(report), "QUICK_ENDPOINT": endpoint},
                       timeout=145, check=True)
        expected = {"VISION_TRIAL": ["board.png"], "VISION_DIRECT": ["board.png"], "VISION_PLANNING": ["board.png"],
                    "VISION_REVISION": ["replacement.png"], "VISION_COMPOSE": ["board.jpg"],
                    "VISION_WEB": ["board.jpg"], "VISION_API": ["board.jpg", "replacement.png"]}
        observed = {receipt["purpose"]: receipt["payloads"] for receipt in self.receipts}
        assert len(observed) == len(self.receipts) == 8, self.receipts
        assert observed["planning"] == []
        for purpose, names in expected.items():
            assert [picture["sha256"] for picture in observed[purpose]] == [
                self.digest((self.directory / name).read_bytes()) for name in names
            ]
        browser = json.loads((report / "vision-browser.json").read_text())
        for channel, purpose in (("direct", "VISION_DIRECT"), ("web", "VISION_WEB"), ("api", "VISION_API")):
            filename = report / f"vision-{channel}.zip"
            with ZipFile(filename) as package:
                manifest = json.loads(package.read("manifest.json"))
                item, = manifest["items"]
                assert manifest["confirmedBy"] and item["reviewer"] and item["version"] == 1
                assert {source["id"] for source in manifest["sources"]} == set(browser[channel]["sourceIDs"])
                assert all(source["pinned"] == 1 for source in manifest["sources"])
                body = package.read(item["path"])
                expected_text = "，".join(picture["result"] for picture in observed[purpose])
                assert body.decode().strip() == expected_text and self.digest(body) == item["sha256"]
        with ZipFile(report / "vision-word.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            original, formatted = manifest["items"]
            assert all(item["reviewer"] and item["version"] == 1 for item in (original, formatted))
            assert package.read(original["path"]).decode().strip() == "绿色标签"
            docx = package.read(formatted["path"])
            assert self.digest(docx) == formatted["sha256"]
            with ZipFile(io.BytesIO(docx)) as file:
                assert "绿色标签" in "".join(ElementTree.fromstring(file.read("word/document.xml")).itertext())
        (report / "vision-provider.json").write_text(json.dumps(self.receipts, ensure_ascii=False, indent=2) + "\n")


def verify_vision_upgrade(pg, report):
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    db = "vision_upgrade_" + uuid4().hex
    pg = [*pg[:-1], "-i", pg[-1]]
    sql = "\n".join(m.read_text() for m in sorted(schema.glob("*.sql")) if m.name[:3] <= "110")
    sql += """
    create temp table fixture(owner uuid,team uuid,project uuid,run uuid,words uuid,opaque uuid,picture uuid);
    insert into fixture select gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid();
    insert into members(id,display_name,email,password_hash,role) select owner,'Operator','vision-upgrade@sample.invalid','','admin' from fixture;
    insert into teams(id,name) select team,'Existing team' from fixture;
    insert into team_members select team,owner from fixture;
    insert into model_resources(name,team_id,created_by) select label,team,owner from fixture cross join (values('text'),('png')) x(label);
    insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,operation)
      select id,1,'https://provider.invalid/v1',name,decode('0304','hex'),name from model_resources;
    insert into projects(id,owner,final_reviewer,team_id,title,stage,plan_version) select project,owner,owner,team,'Existing task','paused',1 from fixture;
    insert into plans(project_id,version,brief,confirmed_by) select project,1,'Original brief',owner from fixture;
    insert into runs(id,project_id,plan_version,actor,charge_team,status,request_key,request_hash,max_calls)
      select run,project,1,owner,team,'paused','previous-run',decode('01','hex'),1 from fixture;
    insert into objects(hash,size_bytes) values(repeat('a',64),42),(repeat('b',64),84);
    insert into sources(id,project_id,name,created_by) select words,project,'original.txt',owner from fixture
      union all select opaque,project,'opaque.png',owner from fixture;
    insert into source_versions(source_id,version,filename,object_hash,media_type,extracted,author)
      select words,1,'original.txt',repeat('a',64),'text/plain; charset=utf-8','Existing text',owner from fixture
      union all select opaque,1,'opaque.png',repeat('b',64),'application/octet-stream',null,owner from fixture;
    insert into plan_sources(project_id,plan_version,source_id,source_version) select project,1,words,1 from fixture;
    """
    sql += (schema / "111_vision_inputs.sql").read_text()
    sql += """
    insert into model_resources(name,team_id,created_by) select 'vision',team,owner from fixture;
    insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,operation)
      select id,1,'https://provider.invalid/v1',name,decode('0506','hex'),'vision' from model_resources where name='vision';
    insert into sources(id,project_id,name,created_by) select picture,project,'checked.png',owner from fixture;
    insert into source_versions(source_id,version,filename,object_hash,media_type,author)
      select picture,1,'checked.png',repeat('b',64),'image/png',owner from fixture;
    select json_build_object(
      'operations',(select array_agg(operation order by operation) from model_revisions),
      'originalKeysIntact',(select count(*) from model_revisions where encrypted_key=decode('0304','hex')),
      'pausedRunUnaffected',(select count(*) from run_source_inputs where run_id=f.run and extracted='Existing text' and version=1 and picture_hash is null),
      'legacyOpaqueAttachmentUnchanged',not planning_sources_readable(project,jsonb_build_array(jsonb_build_object('id',opaque,'version',1))),
      'validatedPictureEligible',planning_sources_readable(project,jsonb_build_array(jsonb_build_object('id',picture,'version',1)))) from fixture f;
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", db], check=True)
    try:
        completed = subprocess.run(pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
                                   input=sql, text=True, capture_output=True)
        if completed.returncode:
            raise RuntimeError(completed.stderr[-1600:])
        observed = json.loads(completed.stdout)
        assert observed == {"operations": ["png", "text", "vision"], "originalKeysIntact": 2,
                            "pausedRunUnaffected": 1, "legacyOpaqueAttachmentUnchanged": True,
                            "validatedPictureEligible": True}, observed
        (report / "vision-upgrade.json").write_text(json.dumps(observed, indent=2) + "\n")
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", db], check=True)


def verify_vision_feishu(req, channel, report):
    service = json.loads((report / "vision-browser.json").read_text())["service"]["id"]
    route = f"services/{service}/entry"
    before = [item["id"] for item in req(route)["recent"]]
    bind = "channels/feishu/bind"
    if not req(bind)["bound"]:
        assert "身份已绑定" in channel.reply(channel.send(req, req(bind, {})["command"]))
    message = channel.send(req, f"发起 {service} 核对视觉标签", chat="oc_vision_redirect")
    reply = channel.reply(message)
    assert f"https://workspace.invalid/?service={service}" in reply
    assert "请打开企业网页附上 PNG 或 JPEG 后确认" in reply and "发送：确认" not in reply
    assert [item["id"] for item in req(route)["recent"]] == before
    (report / "vision-feishu.json").write_text(json.dumps({
        "webLink": True, "confirmationOfferedInChat": False, "newRuns": 0,
        "fixture": "signed synthetic Feishu event", "reply": reply,
    }, ensure_ascii=False, indent=2) + "\n")
