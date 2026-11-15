FROM node:22.23.5-alpine AS web
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.4-alpine AS backend
WORKDIR /src
COPY third_party/ ./third_party/
COPY backend/go.mod backend/go.sum ./backend/
WORKDIR /src/backend
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /alliance .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S alliance && adduser -S -G alliance alliance
WORKDIR /app
COPY --from=backend /alliance /app/alliance
COPY --from=web /src/frontend/dist /app/web
USER alliance
ENV ALLIANCE_STATIC=/app/web ALLIANCE_ADDR=0.0.0.0:8080
EXPOSE 8080 8081
ENTRYPOINT ["/app/alliance"]
