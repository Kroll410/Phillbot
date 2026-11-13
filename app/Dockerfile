# syntax=docker/dockerfile:1
# Вебверсія PhillBot (збірка з тегом `server`). Контекст збірки — тека app/.
#   docker build -t phillbot --build-arg VERSION=dev .

FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -tags server -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /phillbot-server .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -S -g 10001 phillbot \
    && adduser -S -u 10001 -G phillbot -h /data phillbot \
    && mkdir -p /data && chown phillbot:phillbot /data
COPY --from=build /phillbot-server /usr/local/bin/phillbot-server
# Стартуємо від root лише щоб виправити власника тому (на Fly.io том змонтовано як root),
# далі процес працює як непривілейований користувач phillbot.
RUN printf '#!/bin/sh\nchown phillbot:phillbot /data 2>/dev/null\nexec su-exec phillbot "$@"\n' > /entrypoint.sh \
    && chmod +x /entrypoint.sh
ENV PHILLBOT_DATA=/data \
    PHILLBOT_ADDR=0.0.0.0:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
CMD ["phillbot-server"]
