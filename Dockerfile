FROM quay.io/prometheus/busybox:latest
LABEL maintainer="FUSAKLA Martin Chodúr <m.chodur@seznam.cz>"

ARG ARCH="amd64"
ARG OS="linux"
COPY --chown=nobody:nogroup .build/${OS}-${ARCH}/k8s-service-broadcasting /bin/k8s-service-broadcasting
COPY --chown=nobody:nogroup Dockerfile /

EXPOSE 9629
RUN mkdir -p /k8s-service-broadcasting && chown nobody:nogroup /k8s-service-broadcasting
WORKDIR /k8s-service-broadcasting

USER 65534

ENTRYPOINT ["/bin/k8s-service-broadcasting"]
CMD ["--help"]
