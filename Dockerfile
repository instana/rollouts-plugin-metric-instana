FROM alpine:3.29.0
COPY rollouts-plugin-metric-instana /plugin/rollouts-plugin-metric-instana
RUN chmod +x /plugin/rollouts-plugin-metric-instana
