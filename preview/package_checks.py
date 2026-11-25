import io
import zipfile
from xml.etree import ElementTree

ATTACHMENT_LIMIT = 25 << 20
PARTS = {
    "docx": "word/document.xml",
    "pptx": "ppt/presentation.xml",
    "xlsx": "xl/workbook.xml",
}


def check_package(data, fmt, expanded_limit=48 << 20):
    source = io.BytesIO(data) if isinstance(data, bytes) else data
    with zipfile.ZipFile(source) as package:
        files = package.infolist()
        if len(files) > 3000 or sum(f.file_size for f in files) > expanded_limit:
            raise ValueError("Document exceeds conversion limits")
        if (
            PARTS[fmt] not in package.namelist()
            or "[Content_Types].xml" not in package.namelist()
        ):
            raise ValueError("Document type mismatch")
        for f in files:
            name = f.filename.lower()
            if f.flag_bits & 1 or any(
                s in name
                for s in ("vbaproject", "/embeddings/", "/activex/", "/externallinks/")
            ):
                raise ValueError("Active or external content")
            if name.endswith((".rels", ".xml")):
                metadata = name.endswith(".rels") or name == "[content_types].xml"
                if metadata and f.file_size > 1 << 20:
                    raise ValueError("Oversized package metadata")
                raw = package.read(f)
                markup = raw.replace(b"\x00", b"").upper()
                if b"<!DOCTYPE" in markup or b"<!ENTITY" in markup:
                    raise ValueError("Unsupported package metadata")
                if not metadata:
                    continue
                for entry in ElementTree.fromstring(raw):
                    if (
                        entry.get("TargetMode", "").lower() == "external"
                        or "macroenabled" in entry.get("ContentType", "").lower()
                    ):
                        raise ValueError("External or macro-enabled content")
