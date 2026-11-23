FROM debian:bookworm-slim
ARG DEBIAN_MIRROR=http://deb.debian.org
RUN sed -i "s|http://deb.debian.org|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    python3-venv poppler-utils libreoffice-writer libreoffice-impress libreoffice-calc fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/parser \
    && /opt/parser/bin/pip install --no-cache-dir python-docx==1.2.0 openpyxl==3.1.5 python-pptx==1.0.2
COPY server.py package_checks.py extract.py /app/
USER 65534:65534
ENV PREVIEW_HOST=0.0.0.0 PYTHONDONTWRITEBYTECODE=1 HOME=/tmp
CMD ["/opt/parser/bin/python","/app/server.py"]
