"""Verify preview bytes, revision isolation and revocation with trusted test files."""

import io
import subprocess
import zipfile
import urllib.error


def verify_previews(
    req, client, origin, project, sheet_project, sheet_id, connector_python
):
    base = "projects/" + project
    artifacts = req(base)["artifacts"]
    file_url = (
        origin + "/api/" + base + "/artifacts/" + artifacts[0]["id"] + "/file?version="
    )
    with client.open(file_url + "2&preview=1", timeout=30) as pdf:
        assert pdf.headers.get_content_type() == "application/pdf"
        converted = pdf.read()
        assert converted.startswith(b"%PDF-")
    with client.open(file_url + "1") as original:
        first = original.read()
    source = req(base + "/sources?name=copy.pdf", converted)
    access = (
        origin
        + "/api/"
        + base
        + "/sources/"
        + source["id"]
        + "/content?preview=1&version=1"
    )
    with client.open(access) as rendered:
        assert rendered.read() == converted
    req(base + "/sources/" + source["id"], method="DELETE")
    try:
        client.open(access)
        raise AssertionError("revoked preview was readable")
    except urllib.error.HTTPError as error:
        assert error.code == 404
    altered = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(first)) as original, zipfile.ZipFile(
        altered, "w"
    ) as edited:
        for name in original.namelist():
            edited.writestr(name, original.read(name))
        edited.writestr("word/embeddings/content.bin", b"synthetic embedded object")
    source = req(base + "/sources?name=embedded.docx", altered.getvalue())
    try:
        client.open(
            origin
            + "/api/"
            + base
            + "/sources/"
            + source["id"]
            + "/content?version=1&preview=1"
        )
        raise AssertionError("unsupported embedded content converted")
    except urllib.error.HTTPError as error:
        assert error.code == 422

    with client.open(
        origin
        + "/api/"
        + sheet_project
        + "/artifacts/"
        + sheet_id
        + "/file?version=2&preview=1",
        timeout=30,
    ) as pdf:
        assert (
            pdf.headers.get_content_type() == "application/pdf"
            and pdf.read(5) == b"%PDF-"
        )
    slides = subprocess.run(
        [
            str(connector_python),
            "-c",
            """import sys
from pptx import Presentation
deck=Presentation()
for title in ("First page","Second page"):
    slide=deck.slides.add_slide(deck.slide_layouts[0])
    slide.shapes.title.text=title
deck.save(sys.stdout.buffer)
""",
        ],
        capture_output=True,
        check=True,
    ).stdout
    slide_source = req(base + "/sources?name=slides.pptx", slides)
    with client.open(
        origin
        + "/api/"
        + base
        + "/sources/"
        + slide_source["id"]
        + "/content?version=1&preview=1",
        timeout=30,
    ) as pdf:
        assert pdf.read(5) == b"%PDF-"
    picture = subprocess.run(
        [str(connector_python), "-c", "from PIL import Image; import sys; Image.new('RGB', (1, 1), 'blue').save(sys.stdout.buffer, 'PNG')"],
        capture_output=True,
        check=True,
    ).stdout
    saved = req(base + "/sources?name=sample.png", picture)
    assert saved["picture"] and not saved["readable"]
    access = origin + "/api/" + base + "/sources/" + saved["id"] + "/content?version=1&preview=1"
    with client.open(access) as rendered:
        assert rendered.headers.get_content_type() == "image/png" and rendered.read() == picture
