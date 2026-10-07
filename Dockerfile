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
# FOUR BINARIES, AND THE SEPARATION BETWEEN THEM IS THE PRODUCT'S OWN
#
# The console reads, shows and records; it holds no credential and makes no
# outbound call. `tools/run` is what calls a model, and it is the only half ever
# given a key. Baking both in keeps that visible where a deployment can act on
# it: one Deployment with no Secret, one suspended Job with one. Dissolving them
# into a single entrypoint that could do either would take the distinction away
# from whoever is writing the manifest.
#
# The other two close the loop on a machine that has no Go toolchain
# (costcrew#75): `tools/enforce` pushes decided budgets to a TokenFuse control
# plane (the only binary here that changes another system), and
# `tools/idryxsource` writes the roster as the `agents` source idryx asks for.
# The console never invokes either; a launcher runs them as separate
# containers from this same image with `--entrypoint`. Which binaries the image
# holds is declared as `checked.image` in components.json and compared with
# this file by internal/manifest (TestTheDockerfileShipsExactlyTheBinariesTheManifestSaysItDoes).
#
# BASE IMAGES ARE PINNED BY DIGEST
#
# A tag can be repointed under an operator without anyone choosing that; a
# digest cannot (internal/manifest, TestEveryBaseImageIsPinnedByDigest). The tag
# each digest was read from stays in a comment beside it, and dependabot's
# docker ecosystem (.github/dependabot.yml) proposes the next digest as a pull
# request, so the pin is kept fresh by review and not by drift. Read on
# 2026-10-07 with a registry manifest request for the multi-arch index.
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

# golang:1.27-alpine
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
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
      -o /out/costcrew-run ./tools/run \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" \
      -o /out/costcrew-enforce ./tools/enforce \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" \
      -o /out/costcrew-idryxsource ./tools/idryxsource

# gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.title="costcrew"
LABEL org.opencontainers.image.source="https://github.com/TAIPANBOX/costcrew"
# The database, the journal and the signing key are mounted, never baked.
VOLUME ["/var/lib/costcrew"]
COPY --from=build /out/costcrew /usr/local/bin/costcrew
COPY --from=build /out/costcrew-run /usr/local/bin/costcrew-run
COPY --from=build /out/costcrew-enforce /usr/local/bin/costcrew-enforce
COPY --from=build /out/costcrew-idryxsource /usr/local/bin/costcrew-idryxsource
# 65532 is distroless's `nonroot` uid. Numeric on purpose: a kubelet running
# with runAsNonRoot cannot verify a NAME and refuses the container outright.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/costcrew"]
