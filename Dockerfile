# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
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

FROM alpine:3.22
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/yyhoworks/YoLink" \
      org.opencontainers.image.revision=$SOURCE_REVISION
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /data /strm
COPY --from=build /yolink /usr/local/bin/yolink
COPY LICENSE THIRD_PARTY.md /licenses/
COPY licenses /licenses/dependencies
WORKDIR /app
ENV TZ=Asia/Shanghai STRMLINK_DATA=/data STRMLINK_OUTPUT=/strm STRMLINK_LISTEN=:8796
EXPOSE 8796
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["yolink","healthcheck"]
ENTRYPOINT ["/usr/local/bin/yolink"]
