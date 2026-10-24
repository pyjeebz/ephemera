# The relay as a container: a static binary on distroless. Flags are
# set by whatever runs it; see fly.toml for the hosted relay.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /relay ./cmd/relay

FROM gcr.io/distroless/static-debian12
COPY --from=build /relay /relay
ENTRYPOINT ["/relay"]
CMD ["serve"]
