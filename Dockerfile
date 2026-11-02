FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/externaldns-controller ./cmd/externaldns-controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/externaldns-controller /externaldns-controller
USER nonroot
ENTRYPOINT ["/externaldns-controller"]
