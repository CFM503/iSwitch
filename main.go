package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"iswitch/internal/p2p"
	"iswitch/internal/server"
)

var version = "v1.0.21"

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	port := flag.Int("port", 0, "p2p listen port (0 = random)")
	webPort := flag.Int("web-port", 8080, "web UI port")
	dataDir := flag.String("data", "data", "directory for received files")
	wanFlag := flag.Bool("wan", false, "enable WAN discovery (default: LAN only)")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	host, err := p2p.NewHost(*port)
	if err != nil {
		log.Fatalf("create host: %v", err)
	}
	defer host.Close()

	fmt.Printf("Peer ID: %s\n", host.ID().String())
	for _, a := range host.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", a, host.ID())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovery := p2p.NewDiscovery(host, *wanFlag)
	discovery.Start(ctx)
	defer discovery.Stop()

	transferMgr := p2p.NewTransferManager(host, *dataDir)
	transferMgr.Start()

	srv := server.NewServer(host, discovery, transferMgr, *webPort, version)
	srv.SetDiscoveryContext(ctx)
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()

	<-srv.Ready()
	fmt.Printf("Web UI: http://localhost:%d\n", srv.Port())
	fmt.Println("Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
}
