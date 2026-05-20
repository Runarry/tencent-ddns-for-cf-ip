FROM golang:1.26.3-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tencent-ddns ./cmd/server

FROM scratch

WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/tencent-ddns /app/tencent-ddns
COPY config.example.yaml /app/config.example.yaml
ENV CONFIG_FILE=/app/config.yaml
EXPOSE 8080
ENTRYPOINT ["/app/tencent-ddns"]
