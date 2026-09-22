FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/frontress-gateway ./cmd/coordinator

FROM alpine:3.22
RUN adduser -D -H frontress
USER frontress
WORKDIR /app
COPY --from=build /out/frontress-gateway /usr/local/bin/frontress-gateway
COPY coordinator.example.json ./coordinator.json
COPY theater.example.json ./theater.example.json
EXPOSE 27100
ENTRYPOINT ["frontress-gateway"]
CMD ["-config", "/app/coordinator.json"]
