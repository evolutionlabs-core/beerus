FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/beerus ./cmd/beerus

FROM alpine:3.22
RUN adduser -D -u 10001 beerus && mkdir /state && chown 10001:10001 /state
COPY --from=build /out/beerus /usr/local/bin/beerus
USER 10001
ENV BEERUS_DIR=/state
ENTRYPOINT ["beerus"]
