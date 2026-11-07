# syntax=docker/dockerfile:1

#   docker build --build-arg REVISION=$(git rev-parse --short HEAD) -t riverdelta .

# cross-compiles on the build platform, so multi-arch builds need no emulation
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
RUN apk add --no-cache make
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY Makefile ./
COPY app ./app

ARG TARGETOS TARGETARCH
# riverdelta's own revision, logged at startup; the context has no .git for
# the Makefile to derive it from
ARG REVISION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH make build REV=${REVISION}

FROM alpine:3.24
COPY --from=build /build/.bin/riverdelta /riverdelta
USER nobody
EXPOSE 8080
ENTRYPOINT ["/riverdelta"]
CMD ["-config", "/etc/riverdelta/config.yaml"]
