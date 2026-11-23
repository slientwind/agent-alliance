FROM node:22-alpine AS frontend
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=alliance-ui-npm,target=/root/.npm npm ci --prefer-offline --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,id=alliance-runtime-mod,target=/go/pkg/mod go mod download
COPY backend/ ./
RUN --mount=type=cache,id=alliance-runtime-mod,target=/go/pkg/mod --mount=type=cache,id=alliance-linux-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 alliance
COPY --from=backend /out/ /usr/local/bin/
COPY --from=frontend /src/dist/ /opt/alliance/web/
RUN mkdir -p /data/objects && chown alliance /data/objects
USER alliance
ENV API_ADDR=:8080 WEB_ROOT=/opt/alliance/web
EXPOSE 8080
CMD ["api"]
