FROM python:3.12-slim-bookworm
ARG DEBIAN_MIRROR=https://deb.debian.org
ARG PYTHON_INDEX_URL=https://pypi.org/simple
RUN sed -i "s|http://deb.debian.org|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends chromium fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir --index-url "${PYTHON_INDEX_URL}" playwright==1.52.0
COPY browser.py /app/browser.py
USER 65534:65534
ENV BROWSER_HOST=0.0.0.0 HOME=/tmp PYTHONDONTWRITEBYTECODE=1
CMD ["python","/app/browser.py"]
