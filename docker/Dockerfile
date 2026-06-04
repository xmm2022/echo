# syntax=docker/dockerfile:1

# SIDECAR_TOOLS_REF pins the openlist-guangyapan-src commit/tag whose
# cmd/115share2cas producer is compiled into this image. Per R2 we never build
# from the floating feat/cas-tools branch — pin a commit or tag and keep it in
# sync with sidecar.default.min_version and the Echo release notes so a release
# is reproducible. Override with: docker build --build-arg SIDECAR_TOOLS_REF=<ref>
ARG SIDECAR_TOOLS_REF=814736c203e2115bb2dfda597f625c676a5cda74
ARG ECHO_VERSION=v0.1.0-dev

# ---- stage 1: build the echo control-plane binary ----
FROM golang:1.22-alpine AS echo-build
ARG ECHO_VERSION
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${ECHO_VERSION}" -o /out/echo ./cmd/echo
RUN mkdir -p /out/data/ingest /out/data/secrets /out/data/output

# ---- stage 2: build 115share2cas from the pinned sidecar ref ----
# cas139 (Python) is deliberately NOT built into this image (spec §9): it runs in
# a 139 client environment; users prepare its .cas output and manual-import it.
FROM golang:1.24-alpine AS tools-build
ARG SIDECAR_TOOLS_REF
RUN apk add --no-cache git
WORKDIR /sidecar
RUN git clone https://github.com/xmm2022/openlist-guangyapan-src.git . \
 && git checkout "$SIDECAR_TOOLS_REF"
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/115share2cas ./cmd/115share2cas

# ---- stage 3: scratch runtime ----
FROM scratch
COPY --from=echo-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=echo-build /out/echo /usr/local/bin/echo
COPY --from=tools-build /out/115share2cas /usr/local/bin/115share2cas
COPY --from=echo-build --chown=65532:65532 /out/data /data
COPY config.example.yaml /etc/echo/config.yaml

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/echo"]
CMD ["serve", "--config", "/etc/echo/config.yaml"]
