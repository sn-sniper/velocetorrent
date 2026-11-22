//go:build ignore

// Build with:
//   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o VeloceTorrent-Setup.exe ./cmd/setup
//
// See cmd/setup/main.go for the cross-platform VeloceTorrent-Setup.exe implementation.
package main
