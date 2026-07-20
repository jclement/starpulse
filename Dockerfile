# build
FROM golang:1.26-alpine AS build
ARG GIT_SHA=dev
ARG BUILD_DATE=
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags "-s -w -X github.com/jclement/starpulse/internal/site.BuildVersion=$GIT_SHA -X github.com/jclement/starpulse/internal/site.BuildDate=$BUILD_DATE" \
    -o /out/starpulse .

# run — alpine (not scratch) so the optional managed tor works
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tor
COPY --from=build /out/starpulse /usr/local/bin/starpulse
ENV STARPULSE_DATA_DIR=/data
VOLUME /data
EXPOSE 80 443 1965
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["starpulse", "health"]
ENTRYPOINT ["starpulse"]
CMD ["serve"]
