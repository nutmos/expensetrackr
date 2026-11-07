# expensetrackr server image (docs/getting-started.md "Run with Docker Compose").
#
#   docker compose up -d --build --wait     server + Postgres (compose.yaml)
#   docker build -t expensetrackr .         just the image
#
# The server needs a Postgres URL in EXPENSE_DATABASE_URL; compose.yaml sets
# it. Pure Go (CGO off), so the final image is a small Alpine with only the
# binary, CA certificates (Frankfurter is HTTPS) and wget for the health check.

# The build stage runs on the build machine's own platform and cross-compiles
# for the target (TARGETOS/TARGETARCH, set by BuildKit), so a multi-platform
# build (.github/workflows/publish-image.yml: linux/amd64 + linux/arm64)
# compiles natively instead of under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
# Dependencies first, so code changes don't re-download modules.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY pkg ./pkg
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/expense-server ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 expense
COPY --from=build /out/expense-server /usr/local/bin/expense-server
USER expense
# Inside the container the server must listen on every interface; compose
# publishes it on 127.0.0.1 only. GIN_MODE=release silences debug output.
ENV EXPENSE_ADDR=0.0.0.0:8080 \
    GIN_MODE=release
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=12 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/expense-server"]
