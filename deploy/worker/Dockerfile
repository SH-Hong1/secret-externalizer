# Builds the worker binary as a static executable and runs it in a minimal
# alpine image that has git, as a non-root user. Scans read the commit hash
# and the git state of a project with git.

FROM golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/worker ./cmd/worker

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Pinned to the minor version: an Alpine branch keeps only the latest package
# revision, so an exact "-rN" pin stops building at the next security update.
RUN apk add --no-cache git~=2.54

COPY --from=build /out/worker /worker
USER 65532:65532
ENTRYPOINT ["/worker"]
