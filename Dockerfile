# The operator image. `make build-images` cross-compiles the binary for each
# platform into build/images/<arch>/ first, so the image needs no Go toolchain.
# The final image has only the binary and the CA certificates (copied from a
# build stage) so that TLS to a Harbor with a public certificate works.
FROM mirror.gcr.io/library/alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS certs

FROM scratch
ARG TARGETARCH
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY build/images/${TARGETARCH}/harbor-operator /harbor-operator
USER 65532:65532
ENTRYPOINT ["/harbor-operator"]
