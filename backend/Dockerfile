FROM golang:1.22-alpine AS build

WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/creditflow-api ./cmd/api

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=build /out/creditflow-api ./creditflow-api
COPY backend/migrations ./migrations
COPY backend/seed ./seed

ENV PORT=8080
EXPOSE 8080
USER nobody
ENTRYPOINT ["/app/creditflow-api"]
