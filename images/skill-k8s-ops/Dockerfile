# Skill sidecar: k8s-ops
# Provides kubectl for Kubernetes operations skills.
# Runs as non-root (UID 1000) with a read-only rootfs.
FROM alpine:3.20

ARG KUBECTL_VERSION=v1.31.4
ARG TARGETARCH=amd64

# Install kubectl from official release + supporting tools
RUN apk add --no-cache \
    bash \
    curl \
    jq \
    && adduser -D -u 1000 agent \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
       -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

USER 1000
WORKDIR /workspace

# The agent controller sets the command; default to sleep so the
# sidecar stays alive for the lifetime of the agent run.
CMD ["sleep", "infinity"]
