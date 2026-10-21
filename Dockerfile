# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /yolink .

FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/yyhoworks/YoLink" \
      org.opencontainers.image.revision=$SOURCE_REVISION
# Fixed non-privileged runtime user (yolink, 10001:10001). The application only
# writes to /data (STRMLINK_DATA), /strm (STRMLINK_OUTPUT) and the system temp
# directory; bind-mounted host directories must be writable by 10001:10001.
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /data /strm \
 && addgroup -S -g 10001 yolink \
 && adduser -S -u 10001 -G yolink -h /data yolink \
 && chown -R yolink:yolink /data /strm
COPY --from=build /yolink /usr/local/bin/yolink
COPY LICENSE THIRD_PARTY.md /licenses/
COPY licenses /licenses/dependencies
WORKDIR /app
ENV TZ=Asia/Shanghai STRMLINK_DATA=/data STRMLINK_OUTPUT=/strm STRMLINK_LISTEN=:8796
EXPOSE 8796
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["yolink","healthcheck"]
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/yolink"]
