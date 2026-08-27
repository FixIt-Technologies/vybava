# luko.to redirector — built from the repo root:
#   docker build -f deploy/shrt/Dockerfile .
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/vybava ./cmd/vybava

FROM alpine:3.20
RUN adduser -D -u 10001 shrt && mkdir -p /data && chown shrt /data
COPY --from=build /out/vybava /usr/local/bin/vybava
USER shrt
VOLUME /data
EXPOSE 8080
# LUKO_MINT_TOKEN comes from the deployment environment.
CMD ["vybava", "shrt", "serve", "--addr", ":8080", "--store", "/data/links.jsonl"]
