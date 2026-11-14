FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/go-exam ./cmd/go-exam \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/fiatcode-gh/go-exam"
COPY --from=build /out/go-exam /go-exam
COPY --from=build --chown=65532:65532 /out/data /data
ENV GOEXAM_DATA_DIR=/data GOEXAM_LISTEN_ADDR=:8080
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/go-exam"]
