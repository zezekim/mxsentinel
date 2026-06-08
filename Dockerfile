# Builds all MX Sentinel Go service binaries into one minimal image. The schemas,
# migrations, and event contracts are embedded in the binaries (go:embed), so the final
# image needs nothing but the static executables. Each compose service selects its binary
# via `command` (e.g. /usr/local/bin/apid).

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off -> fully static binaries that run on distroless/static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
USER nonroot:nonroot
# No default service; compose sets `command: ["/usr/local/bin/<svc>"]`.
ENTRYPOINT []
