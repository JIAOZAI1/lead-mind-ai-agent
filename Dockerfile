# syntax=docker/dockerfile:1

FROM golang:1.25.6-alpine AS build
WORKDIR /src

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20 AS runtime
WORKDIR /app

COPY --from=build /out/server /app/server

EXPOSE 8080

ENTRYPOINT ["/app/server"]
