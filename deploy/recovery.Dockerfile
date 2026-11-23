FROM golang:1.27-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /recovery ./cmd/recovery

FROM pgvector/pgvector:0.8.0-pg16
COPY --from=build /recovery /usr/local/bin/recovery
USER 10001:10001
ENTRYPOINT ["recovery"]
