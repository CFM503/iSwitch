package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

// TestShortCodeLookup creates two in-process peers connected via DHT,
// advertises one peer's short code, and verifies the other can find it.
func TestShortCodeLookup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// --- Create two hosts ---
	h1, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("host1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("host2: %v", err)
	}
	defer h2.Close()

	code1 := shortCode(h1.ID())
	code2 := shortCode(h2.ID())
	t.Logf("Host1 ID: %s  short code: %s", h1.ID(), code1)
	t.Logf("Host2 ID: %s  short code: %s", h2.ID(), code2)

	// --- Connect h2 to h1 directly ---
	h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), 10*time.Minute)
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("direct connect: %v", err)
	}
	t.Log("Direct connection established")

	// --- Create DHTs (client-server so they can find each other) ---
	kdht1, err := dht.New(ctx, h1, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		t.Fatalf("dht1: %v", err)
	}
	defer kdht1.Close()

	kdht2, err := dht.New(ctx, h2, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		t.Fatalf("dht2: %v", err)
	}
	defer kdht2.Close()

	// Bootstrap both DHTs
	if err := kdht1.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap1: %v", err)
	}
	if err := kdht2.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap2: %v", err)
	}

	// Wait for DHT routing to settle
	time.Sleep(2 * time.Second)

	// --- Host1 advertises its short code ---
	rd1 := routing.NewRoutingDiscovery(kdht1)
	ns1 := "iswitch-shortcode-" + code1
	rd1.Advertise(ctx, ns1)
	t.Logf("Host1 advertising under namespace: %s", ns1)

	// Give advertise time to propagate
	time.Sleep(3 * time.Second)

	// --- Host2 looks up Host1 by short code ---
	rd2 := routing.NewRoutingDiscovery(kdht2)
	findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
	defer findCancel()

	peersCh, err := rd2.FindPeers(findCtx, ns1)
	if err != nil {
		t.Fatalf("FindPeers error: %v", err)
	}

	found := false
	for pi := range peersCh {
		if pi.ID == h1.ID() {
			found = true
			t.Logf("SUCCESS: Found Host1 via DHT short code lookup! Addrs: %v", pi.Addrs)
			break
		}
	}

	if !found {
		t.Fatalf("FAIL: Could not find Host1 (code %s) via DHT lookup", code1)
	}

	// --- Also test via Discovery.FindPeerByShortCode ---
	disc1 := NewDiscovery(h1, true)
	disc1.Start(ctx)
	defer disc1.Stop()

	disc2 := NewDiscovery(h2, true)
	disc2.Start(ctx)
	defer disc2.Stop()

	// Wait for discovery to initialize and advertise
	time.Sleep(8 * time.Second)

	result, err := disc2.FindPeerByShortCode(ctx, code1)
	if err != nil {
		t.Fatalf("FindPeerByShortCode error: %v", err)
	}
	if result.ID != h1.ID() {
		t.Fatalf("FindPeerByShortCode returned wrong peer: got %s, want %s", result.ID, h1.ID())
	}
	t.Logf("SUCCESS: FindPeerByShortCode found correct peer %s with addrs %v", result.ID, result.Addrs)
}
