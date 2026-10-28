FROM golang:1.27.3 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.24

RUN apk add --no-cache ca-certificates

COPY --from=build /out/api /usr/local/bin/api

USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
