// Ephemera is a single Go module serving two binaries:
//   ephemera - sender-side daemon, runs on the sender's machine
//   relay    - public-facing server, pairs with daemon over WebSocket
module ephemera.run/ephemera

go 1.26

require github.com/coder/websocket v1.8.15 // indirect
