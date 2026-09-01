# CostCrew as an image, so that running it does not mean building it.
#
# This file lived in TAIPANBOX/stack-k8s as images/costcrew.Dockerfile from the
# day the finops plane got a cluster shape, because that is where the shape was
# written. It belongs here instead: this repository is the only thing it builds
# from, and one file describing one repository's build should sit in that
# repository. The copy over there is deleted in the same change that points its
# manifests at the published image, so there is one owner rather than two
# copies that drift.
#
# TWO BINARIES, AND THAT IS THE PRODUCT'S OWN SEPARATION
#
# The console reads, shows and records; it holds no credential and makes no
# outbound call. `tools/run` is what calls a model, and it is the only half ever
# given a key. Baking both in keeps that visible where a deployment can act on
# it: one Deployment with no Secret, one suspended Job with one. Dissolving them
# into a single entrypoint that could do either would take the distinction away
# from whoever is writing the manifest.
#
# CROSS-COMPILED, NOT EMULATED
#
# The build stage is pinned to BUILDPLATFORM and Go is told the target, so an
# arm64 image is produced by a compiler running natively on the runner rather
# than by an amd64 toolchain under QEMU. For a Go program that is the whole of
# what multi-arch costs; the emulated route is minutes per architecture and
# buys nothing here.
#
# CGO is off for the same reason the estate's other Go images have it off, and
# it is free here: the SQLite driver is pure Go, so there is no C dependency to
# carry into a static runtime.
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ENV GOTOOLCHAIN=auto
WORKDIR /src
# Dependencies first, so a code-only change does not re-download the module
# graph on every build.
COPY go.mod go.su[m] ./
RUN go mod download
COPY . ./
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" \
      -o /out/costcrew ./cmd/costcrew \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" \
      -o /out/costcrew-run ./tools/run

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="costcrew"
LABEL org.opencontainers.image.source="https://github.com/TAIPANBOX/costcrew"
# The database, the journal and the signing key are mounted, never baked.
VOLUME ["/var/lib/costcrew"]
COPY --from=build /out/costcrew     /usr/local/bin/costcrew
COPY --from=build /out/costcrew-run /usr/local/bin/costcrew-run
# 65532 is distroless's `nonroot` uid. Numeric on purpose: a kubelet running
# with runAsNonRoot cannot verify a NAME and refuses the container outright.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/costcrew"]
