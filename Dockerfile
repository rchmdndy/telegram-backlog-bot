FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN test "$TARGETOS" = linux && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/backlog-bot ./cmd/backlog-bot

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
COPY --from=build /out/backlog-bot /backlog-bot
USER 10001:10001
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["/backlog-bot"]
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=20s CMD ["/backlog-bot", "healthcheck"]
