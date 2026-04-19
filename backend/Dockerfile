FROM golang:1.23-alpine AS build

WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/mapcluster ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/mapcluster /app/mapcluster
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/mapcluster"]
