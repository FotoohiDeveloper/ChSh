# syntax=docker/dockerfile:1.7
# Base images are build arguments so they can point at a private registry or
# mirror when the public ones are not reachable from the server.
ARG GO_IMAGE=golang:1.26-alpine
# Empty image: nothing needs gcr.io (blocked from some networks). CA certificates and time zones are copied from the build stage.
ARG RUNTIME_IMAGE=scratch

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/chador ./cmd/chador

# No shell, no package manager, no root: a compromised process has almost nothing to work with.
FROM ${RUNTIME_IMAGE}
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /zoneinfo.zip
ENV ZONEINFO=/zoneinfo.zip
COPY --from=build /out/chador /usr/local/bin/chador
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 CMD ["/usr/local/bin/chador", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/chador"]
CMD ["serve"]
