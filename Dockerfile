FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION
RUN test -n "$VERSION" \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/panda ./cmd/panda

FROM alpine:3.23

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 panda \
    && adduser -S -D -H -u 10001 -G panda panda \
    && mkdir /data \
    && chown panda:panda /data \
    && chmod 750 /data
COPY --from=build /out/panda /usr/local/bin/panda

ENV ADDR=:8080 \
    DATA_DIR=/data
USER panda:panda
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -T 4 -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["panda"]
CMD ["serve"]
