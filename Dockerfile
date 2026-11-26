ARG DOCKER_REGISTRY=docker.io
FROM ${DOCKER_REGISTRY}/golang:1.27.4 AS build
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s" -a -installsuffix cgo -o /main
# The heartbeat emitter ships in the same image: a CronJob runs it by
# overriding the entrypoint with `command: ["/heartbeat"]`, so the emitter
# cannot drift from the watcher's tag or search expression.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s" -a -installsuffix cgo -o /heartbeat ./cmd/heartbeat
# The detector ships in the same image too, so emitter and checker cannot drift
# apart on the tag or the search expression — a mismatch would report ingestion
# dead forever, indistinguishable from the real failure.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s" -a -installsuffix cgo -o /check-heartbeat ./cmd/check-heartbeat
# The one-shot pass ships in the same image so a Schedule-driven Job cannot drift
# from the watcher's tag or query. The entrypoint selects it when MODE=once; the
# default stays the continuous watcher the StatefulSet runs.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s" -a -installsuffix cgo -o /run-once ./cmd/run-once
CMD ["/bin/bash"]

FROM ${DOCKER_REGISTRY}/alpine:3.23 AS alpine
RUN apk --no-cache add ca-certificates curl bash \
 && rm -rf /tmp/*

FROM alpine
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY --from=build /main /main
COPY --from=build /heartbeat /heartbeat
COPY --from=build /check-heartbeat /check-heartbeat
COPY --from=build /run-once /run-once
COPY hack/entrypoint.sh /entrypoint
RUN chmod 0755 /entrypoint
ENV ZONEINFO=/zoneinfo.zip
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /
ENV BUILD_GIT_COMMIT=${BUILD_GIT_COMMIT}
ENV BUILD_DATE=${BUILD_DATE}
ENTRYPOINT ["/entrypoint"]
