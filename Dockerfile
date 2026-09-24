FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mockpsp ./cmd/mockpsp \
    && GOBIN=/out go install github.com/pressly/goose/v3/cmd/goose@v3.27.1

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S -G app app

WORKDIR /app

COPY --from=build /out/api /app/api
COPY --from=build /out/mockpsp /app/mockpsp
COPY --from=build /out/goose /app/goose
COPY --from=build /src/internal/db/migrations /app/migrations

USER app

EXPOSE 8080 8081

ENTRYPOINT ["/app/api"]
