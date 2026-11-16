# Agent Reliability Lab — one image: API + agent WebSocket + embedded UI + worker.
FROM node:22-alpine AS ui
WORKDIR /ui
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.26 AS build
ARG VERSION=0.3.0
WORKDIR /src
COPY go.mod go.sum ./
COPY sdk/go/go.mod sdk/go/go.sum ./sdk/go/
RUN go mod download
COPY . .
COPY --from=ui /ui/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X go.vicoworks.com/agent-reliability-lab/internal/server.Version=${VERSION}" -o /out/arl . \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/arl-worker ./cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="Agent Reliability Lab" \
      org.opencontainers.image.description="Live tracing, replay workbench and evaluation for AI agents in development" \
      org.opencontainers.image.source="https://github.com/VicoAritonang/lab-of-night-and-flame" \
      org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /app
COPY --from=build /out/arl /app/arl
COPY --from=build /out/arl-worker /app/arl-worker
# All-in-one development defaults; override for the split AWS deployment.
ENV ARL_ADDR=:8000 ARL_AUTO_MIGRATE=true ARL_LISTEN_ON_START=true GOMEMLIMIT=256MiB
EXPOSE 8000
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=4s --start-period=20s --retries=3 CMD ["/app/arl", "healthcheck"]
ENTRYPOINT ["/app/arl"]
