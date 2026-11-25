FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py kb_models.py .
USER 65534:65534
ENV RETRIEVAL_HOST=0.0.0.0 PYTHONDONTWRITEBYTECODE=1
CMD ["python","server.py"]
