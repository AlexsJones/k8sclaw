# Skill sidecar: sre-observability
# Provides kubectl + curl + jq for Prometheus/Loki/Kubernetes observability workflows.
# Runs as non-root (UID 1000) with the shared tool-executor loop.
FROM alpine:3.20

ARG KUBECTL_VERSION=v1.31.4
ARG TARGETARCH=amd64

RUN apk add --no-cache \
    bash \
    curl \
    jq \
    && adduser -D -u 1000 agent \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
       -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

COPY images/skill-k8s-ops/tool-executor.sh /usr/local/bin/tool-executor.sh
RUN chmod +x /usr/local/bin/tool-executor.sh

USER 1000
WORKDIR /workspace

CMD ["/usr/local/bin/tool-executor.sh"]
