FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/answerbox ./cmd/answerbox \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/fiatcode-gh/answerbox"
COPY --from=build /out/answerbox /answerbox
COPY --from=build --chown=65532:65532 /out/data /data
ENV ANSWERBOX_DATA_DIR=/data ANSWERBOX_LISTEN_ADDR=:8080
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/answerbox"]
