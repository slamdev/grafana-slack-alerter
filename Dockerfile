FROM gcr.io/distroless/static:nonroot
WORKDIR /
ADD grafana-slack-alerter grafana-slack-alerter
USER 65532:65532

ENTRYPOINT ["/grafana-slack-alerter"]
