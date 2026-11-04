FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/frontress-gc ./cmd/frontress-gc

FROM alpine:3.22
RUN adduser -D -H frontress && mkdir -p /data && chown frontress /data
USER frontress
WORKDIR /app
COPY --from=build /out/frontress-gc /usr/local/bin/frontress-gc
COPY gc.example.json ./gc.json
COPY theater.example.json ./theater.example.json
ENV GC_DATA_DIR=/data
VOLUME /data
EXPOSE 27100
HEALTHCHECK --interval=10s --timeout=3s CMD wget -qO- http://127.0.0.1:27100/healthz || exit 1
ENTRYPOINT ["frontress-gc"]
CMD ["-config", "/app/gc.json"]
