ARG GO_IMAGE=golang:1.27.3-bookworm
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY app/backend/go.mod app/backend/go.sum ./
RUN go mod download
COPY app/backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -o /out/demo-auth ./cmd/demo-auth

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 app
WORKDIR /workspace
COPY --from=build /out/server /usr/local/bin/server
COPY docker/backend-entrypoint.sh /usr/local/bin/backend-entrypoint.sh
USER 10001:10001
EXPOSE 8090
ENTRYPOINT ["sh", "/usr/local/bin/backend-entrypoint.sh"]

FROM python:3.12.14-slim-bookworm AS bootstrap
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 app
WORKDIR /workspace
COPY app/backend/go.mod app/backend/go.mod
RUN git init --quiet \
    && printf '.local/\n' > .gitignore \
    && mkdir -p /workspace/.local /run/rzd-database \
    && chmod 700 /workspace/.local \
    && chmod 755 /run/rzd-database \
    && chown -R 10001:10001 /workspace /run/rzd-database
COPY --from=build /out/demo-auth /usr/local/bin/demo-auth
COPY docker/init-runtime.py docker/show-key.py /usr/local/bin/
USER 10001:10001
ENTRYPOINT ["python", "/usr/local/bin/init-runtime.py"]
