FROM python:3.12-slim-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends fonts-noto-cjk fontconfig \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt /tmp/alliance-sandbox-requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: -r /tmp/alliance-sandbox-requirements.txt \
    && pip check \
    && rm /tmp/alliance-sandbox-requirements.txt

RUN mkdir -p /workspace/inputs /workspace/outputs
WORKDIR /workspace
USER 65534:65534
ENV HOME=/tmp MPLCONFIGDIR=/tmp/matplotlib MPLBACKEND=Agg OPENBLAS_NUM_THREADS=1 OMP_NUM_THREADS=1
ENTRYPOINT ["python", "-I", "-B"]
CMD ["--version"]
