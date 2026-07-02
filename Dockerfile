ARG VERSION=dev

FROM golang:1.25-alpine AS builder

ARG VERSION

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X github.com/jancernik/deeplo/internal/build.Version=${VERSION}" \
        -o /out/deeplo ./cmd/deeplo

FROM alpine:3

ENV DEEPLO_DATA_DIR=/var/lib/deeplo

RUN apk add --no-cache git openssh-client ca-certificates bash bash-completion && \
    rm -rf /usr/share/bash-completion/completions/* && \
    addgroup -g 1000 -S deeplo && \
    adduser  -u 1000 -S -G deeplo deeplo && \
    mkdir -p /var/lib/deeplo /run/deeplo && \
    chown deeplo:deeplo /var/lib/deeplo /run/deeplo

COPY --from=builder /out/deeplo /usr/local/bin/deeplo

RUN /usr/local/bin/deeplo completion bash > /etc/bash/deeplo.sh

USER deeplo

EXPOSE 8470

ENTRYPOINT ["/usr/local/bin/deeplo"]
CMD ["daemon"]
