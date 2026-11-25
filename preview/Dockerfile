FROM python:3.12-slim-bookworm AS knowledge-parser
ARG DEBIAN_MIRROR=https://deb.debian.org
ARG PYTHON_INDEX_URL=https://pypi.org/simple
RUN sed -i "s|http://deb.debian.org|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    poppler-utils tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-eng fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir --index-url "${PYTHON_INDEX_URL}" pdfplumber==0.11.8 python-docx==1.2.0 openpyxl==3.1.5 python-pptx==1.0.2
COPY kb_extract.py kb_layout.py kb_pdf.py package_checks.py extract.py /app/
USER 65534:65534
ENV KB_PARSE_HOST=0.0.0.0 PYTHONDONTWRITEBYTECODE=1 HOME=/tmp
CMD ["python","/app/kb_extract.py"]

FROM debian:bookworm-slim AS preview
ARG DEBIAN_MIRROR=http://deb.debian.org
RUN sed -i "s|http://deb.debian.org|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    python3-venv poppler-utils libreoffice-writer libreoffice-impress libreoffice-calc fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/parser \
    && /opt/parser/bin/pip install --no-cache-dir python-docx==1.2.0 openpyxl==3.1.5 python-pptx==1.0.2
COPY server.py package_checks.py extract.py anchor.py /app/
USER 65534:65534
ENV PREVIEW_HOST=0.0.0.0 PYTHONDONTWRITEBYTECODE=1 HOME=/tmp
CMD ["/opt/parser/bin/python","/app/server.py"]
