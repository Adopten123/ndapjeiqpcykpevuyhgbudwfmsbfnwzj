FROM python:3.12.14-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /workspace
COPY app/compute/requirements-lock.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: -r /tmp/requirements.txt \
    && useradd --create-home --uid 10001 app
COPY app/simulator/ app/simulator/
USER 10001:10001
EXPOSE 8094
CMD ["python", "app/simulator/main.py", "--host", "0.0.0.0"]
