# Builds the worker binary as a static executable and runs it in a minimal
# alpine image that has git, as a non-root user. Scans read the commit hash
# and the git state of a project with git, and run the betterleaks and
# opengrep executables pinned in scanners.lock. Both are statically linked,
# so they run on alpine's musl. The rules they run with are in /rules.
# Apply checks and applies read the git state of a project with git and
# never run a git command that changes the repository. compose runs this
# image twice: as the worker, which only scans and has only the read-only
# workspace mount, and as the applier, which only applies, runs no scanner,
# and has the read-write mount it writes the project's files through.
# compose may run both as another user (SE_UID, SE_GID) so the files an
# apply writes keep the owner of the workspace.

FROM golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/worker ./cmd/worker

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS scanners

# BuildKit sets these for the platform being built. The classic builder
# does not, and then the stage runs on that platform, so its own
# architecture is the one to fetch for.
ARG TARGETOS=linux
ARG TARGETARCH

# Only fetches the scanners; nothing from this stage but /out reaches the
# final image. Pinned to the major version for the same reason as git below.
RUN apk add --no-cache curl~=8
COPY deploy/worker/fetch-scanners.sh deploy/worker/scanners.lock /fetch/
RUN set -eu; \
	arch="${TARGETARCH:-}"; \
	if [ -z "$arch" ]; then \
		case "$(apk --print-arch)" in \
		aarch64) arch=arm64 ;; \
		x86_64) arch=amd64 ;; \
		*) echo "no scanners for $(apk --print-arch)" >&2; exit 1 ;; \
		esac; \
	fi; \
	sh /fetch/fetch-scanners.sh "$TARGETOS" "$arch" /out

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Pinned to the minor version: an Alpine branch keeps only the latest package
# revision, so an exact "-rN" pin stops building at the next security update.
RUN apk add --no-cache git~=2.54

COPY --from=build /out/worker /worker
COPY --from=scanners /out/betterleaks /out/opengrep /usr/local/bin/
COPY rules /rules
USER 65532:65532
ENTRYPOINT ["/worker"]
