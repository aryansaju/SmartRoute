# Build:  docker build -f deploy/Dockerfile --target gateway -t smartroute-gateway .
# Requires go.sum: run `go mod tidy` once and commit it.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mockupstream ./cmd/mockupstream

FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=build /out/gateway /gateway
EXPOSE 8080
ENTRYPOINT ["/gateway"]

FROM gcr.io/distroless/static-debian12:nonroot AS mockupstream
COPY --from=build /out/mockupstream /mockupstream
EXPOSE 9000
ENTRYPOINT ["/mockupstream"]
