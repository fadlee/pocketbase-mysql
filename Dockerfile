FROM node:25-bookworm AS ui-build

WORKDIR /src/ui

COPY ui/package.json ui/package-lock.json ./
COPY ui/dprint.json ./
COPY ui/dprint-typescript-0.95.15.wasm ./
COPY ui/index.html ./
COPY ui/vite.config.js ./
COPY ui/public ./public
COPY ui/src ./src

RUN npm ci && npm run build

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

COPY --from=ui-build /src/ui/dist ./ui/dist

ENV CGO_ENABLED=0
RUN go build -o /out/pocketbase-mysql ./examples/base

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates tzdata \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/pocketbase-mysql /app/pocketbase-mysql

EXPOSE 8090
VOLUME ["/pb_data"]

ENTRYPOINT ["/app/pocketbase-mysql"]
CMD ["serve", "--dir", "/pb_data", "--http", "0.0.0.0:8090"]
