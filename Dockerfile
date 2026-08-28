# luko.to redirector — repo-root Dockerfile: deployik ships the Dockerfile's
# directory as the build context, so it must sit at the root to see go.mod/go.sum.
#   docker build .
FROM golang:1.26-alpine AS build
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
