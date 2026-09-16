# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/curvature-relay ./relay

FROM scratch
COPY --from=builder /out/curvature-relay /curvature-relay

VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/curvature-relay"]
CMD ["-addr", ":8080", "-base", "https://relay.example.com", "-data", "/data"]