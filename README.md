# ephemera

Ephemera creates a shareable link before the file is uploaded. The link streams live from the sender's disk, only the byte ranges the receiver asks for, while the file uploads to the relay in the background. Once it's all there the sender can go offline. When the link expires, the relay deletes its copy.

## Documentation

- [Wire protocol](docs/protocol.md)
- [Architecture](docs/architecture.md)
  - [tldraw diagram](docs/architecture.tldr)
- [Real-device testing](docs/testing.md)
- [Deploying a relay](docs/deploy.md) (Caddy and systemd configs in `deploy/`)

## License

MIT. See [LICENSE](LICENSE).
