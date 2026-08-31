FROM alpine:3.21

RUN addgroup -S -g 1000 agent && adduser -S -D -H -u 1000 -G agent agent

COPY --chmod=0555 examples/harness-reference/adapter.sh /usr/local/bin/sympozium-reference-adapter

USER 1000:1000
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/sympozium-reference-adapter"]
