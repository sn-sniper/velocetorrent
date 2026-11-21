package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
)

func main() {
	var (
		ipcAddr       = flag.String("addr", ipc.DefaultTCPAddr, "Local HTTP/WebSocket IPC bind address")
		unixPath      = flag.String("socket", ipc.DefaultUnixSocketPath(), "Local Unix domain socket path")
		outputDir     = flag.String("dir", "", "Default download output directory")
		peerAddr      = flag.String("peer-addr", "0.0.0.0:0", "BitTorrent TCP/uTP peer wire listen address")
		stateDB       = flag.String("state-db", "", "Path to fast-resume state.db file")
		blocklistPath = flag.String("blocklist", "", "Path to CIDR / PeerGuardian IP blocklist file")
		onCompleteCmd = flag.String("on-complete-cmd", "", "Shell command to run when a torrent finishes")
		onCompleteWH  = flag.String("on-complete-webhook", "", "Webhook URL to POST when a torrent finishes")
	)
	flag.Parse()

	eng, err := engine.NewEngine(engine.Config{
		DefaultOutputDir:  *outputDir,
		ListenAddr:        *peerAddr,
		StateDBPath:       *stateDB,
		BlocklistPath:     *blocklistPath,
		OnCompleteCmd:     *onCompleteCmd,
		OnCompleteWebhook: *onCompleteWH,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] Engine initialization failed: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	srv := ipc.NewServer(eng, *ipcAddr, *unixPath)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[FATAL] IPC server failed to start: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	fmt.Fprintf(os.Stdout, "VeloceTorrent Daemon online | IPC: http://%s | Socket: %s | PeerPort: %d | Dir: %s | Token: %s\n",
		srv.TCPAddr(), srv.UnixSocketPath(), eng.ListenPort(), eng.DefaultOutputDir(), srv.AuthTokenPath())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Fprintln(os.Stdout, "\nShutting down VeloceTorrent Daemon (flushing state.db & NVMe buffers)...")
}
