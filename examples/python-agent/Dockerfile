# Build context: repository root (see examples/compose/docker-compose.yml).
FROM python:3.12-slim
WORKDIR /app
COPY sdk/python /sdk
RUN pip install --no-cache-dir /sdk
COPY examples/python-agent/agent.py .
ENV PYTHONUNBUFFERED=1
EXPOSE 8082
CMD ["python", "agent.py"]
