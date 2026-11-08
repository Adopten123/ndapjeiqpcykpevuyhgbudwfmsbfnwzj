FROM python:3.12.14-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /workspace
COPY app/compute/requirements-lock.txt /tmp/compute-requirements.txt
COPY app/compute/planner/requirements-lock.txt /tmp/planner-requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: -r /tmp/compute-requirements.txt -r /tmp/planner-requirements.txt \
    && useradd --create-home --uid 10001 app
COPY app/compute/ app/compute/
USER 10001:10001
EXPOSE 8091
CMD ["python", "app/compute/main.py", "--host", "0.0.0.0"]
