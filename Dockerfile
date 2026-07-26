FROM docker.io/library/golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS build

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/homelab-forgejo-webhook \
    ./cmd/homelab-forgejo-webhook

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/homelab-forgejo-webhook /homelab-forgejo-webhook
LABEL org.opencontainers.image.description="Forgejo webhook verifier for git-pages"
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/homelab-forgejo-webhook"]
