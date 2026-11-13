# The hosted central server, api.ephemera.run, on Fly.io. Self-hosters
# don't need this: it's one way to run ephemera-server, not the only one.
# Build from the repository root:
#   fly deploy --config deploy/fly/fly.toml --ha=false
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /ephemera-server ./cmd/ephemera-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ephemera-server /ephemera-server
USER nonroot
EXPOSE 7700
ENTRYPOINT ["/ephemera-server", "serve", "--listen", "0.0.0.0:7700"]
