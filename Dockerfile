# The operator image. `make build-images` cross-compiles the binary for each
# platform into build/images/<arch>/ first, so the image needs no Go toolchain.
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
ARG TARGETARCH
COPY build/images/${TARGETARCH}/harbor-operator /harbor-operator
USER 65532:65532
ENTRYPOINT ["/harbor-operator"]
