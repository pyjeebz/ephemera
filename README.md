# ephemera

Ephemera creates a shareable link before the file is uploaded. The sender daemon streams only the byte ranges the receiver requests; the relay keeps no durable copy, and the link expires.

## Documentation

- [Wire protocol](docs/protocol.md)
- [Architecture](docs/architecture.md)
  - [tldraw diagram](docs/architecture.tldr)
- [Real-device testing](docs/testing.md)

## License

MIT. See [LICENSE](LICENSE).
