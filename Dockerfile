FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN test "$TARGETOS" = linux && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/backlog-bot ./cmd/backlog-bot

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/backlog-bot /backlog-bot
USER 10001:10001
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["/backlog-bot"]
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=20s CMD ["/backlog-bot", "healthcheck"]
