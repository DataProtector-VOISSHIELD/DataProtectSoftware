FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package.json ./package.json
RUN npm install --no-audit --no-fund
COPY web ./
RUN npm run typecheck && npm run build

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY --from=frontend /web/dist ./cmd/controlplane/ui/dist
RUN gofmt -w cmd/controlplane/main.go cmd/controlplane/main_test.go cmd/agent/main.go
RUN go mod tidy
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/controlplane ./cmd/controlplane
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/backup-agent ./cmd/agent

FROM alpine:3.22
RUN addgroup -S backup && adduser -S -G backup backup
WORKDIR /app
COPY --from=build /out/controlplane /app/controlplane
COPY --from=build /out/backup-agent /app/backup-agent
USER backup
EXPOSE 8080
ENTRYPOINT ["/app/controlplane"]

