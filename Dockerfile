# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN cp "/src/1월5주간업무보고_AI엔지니어링.pptx" /src/cmd/weekly/templates/reference.pptx
COPY --from=frontend /src/frontend/dist/ ./cmd/weekly/web/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o /out/weekly ./cmd/weekly

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S weekly && adduser -S -G weekly -h /var/lib/weekly weekly \
    && mkdir -p /var/lib/weekly && chown -R weekly:weekly /var/lib/weekly
COPY --from=backend /out/weekly /usr/local/bin/weekly
USER weekly
EXPOSE 8080
VOLUME ["/var/lib/weekly"]
ENTRYPOINT ["/usr/local/bin/weekly"]
