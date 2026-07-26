# syntax=docker/dockerfile:1
FROM golang:1.25.12 AS builder
WORKDIR /src
COPY kubernetes/go.mod kubernetes/go.sum ./kubernetes/
COPY components/idle-controller/go.mod components/idle-controller/go.sum ./components/idle-controller/
RUN cd components/idle-controller && go mod download
COPY kubernetes ./kubernetes
COPY components/idle-controller ./components/idle-controller
RUN cd components/idle-controller && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /idle-controller ./cmd/idle-controller

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /idle-controller /idle-controller
USER 65532:65532
ENTRYPOINT ["/idle-controller"]
