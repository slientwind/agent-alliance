"""Read the delivered originals independently of the project view and model reply."""

from hashlib import sha256
import json
from zipfile import ZipFile


def verify_draft_deliveries(folder, mode):
    report = json.loads((folder / f"draft-revision-{mode}.json").read_text())
    for stem, answer in (("draft-revision", report["delivery"]), ("draft-project-gap", report["projectGap"]["answer"])):
        with ZipFile(folder / f"{stem}-{mode}.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            assert manifest["edition"] == 1 and manifest["confirmedBy"] and manifest["confirmedAt"]
            item, = manifest["items"]
            assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
            content = package.read(item["path"])
            assert sha256(content).hexdigest() == item["sha256"]
            assert content.decode() == answer
