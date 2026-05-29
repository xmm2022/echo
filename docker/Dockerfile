FROM golang:1.22 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/echo ./cmd/echo
RUN mkdir -p /out/data/ingest /out/data/secrets /out/data/output

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/echo /echo
COPY --from=build --chown=65532:65532 /out/data /data
COPY config.example.yaml /etc/echo/config.yaml

USER 65532:65532
EXPOSE 8080

ENTRYPOINT ["/echo"]
CMD ["serve", "--config", "/etc/echo/config.yaml"]
