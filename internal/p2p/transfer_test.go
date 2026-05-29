package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

// TestShortCodeTransfer creates two peers, connects via DHT short code,
// then sends a file and verifies it arrives intact.
func TestShortCodeTransfer(t *testing.T) {
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
	t.Logf("Host1 (sender):    %s  code: %s", h1.ID().ShortString(), code1)
	t.Logf("Host2 (receiver):  %s  code: %s", h2.ID().ShortString(), code2)

	// --- Connect h2 to h1 directly ---
	h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), 10*time.Minute)
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("direct connect: %v", err)
	}
	t.Log("Direct connection established")

	// --- Create DHTs ---
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

	if err := kdht1.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap1: %v", err)
	}
	if err := kdht2.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap2: %v", err)
	}
	time.Sleep(2 * time.Second)

	// --- Host1 advertises its short code ---
	rd1 := routing.NewRoutingDiscovery(kdht1)
	ns1 := "iswitch-shortcode-" + code1
	rd1.Advertise(ctx, ns1)
	t.Logf("Host1 advertising: %s", ns1)
	time.Sleep(3 * time.Second)

	// --- Host2 looks up Host1 by short code ---
	rd2 := routing.NewRoutingDiscovery(kdht2)
	findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
	defer findCancel()

	peersCh, err := rd2.FindPeers(findCtx, ns1)
	if err != nil {
		t.Fatalf("FindPeers: %v", err)
	}

	var targetPeer *peer.AddrInfo
	for pi := range peersCh {
		if pi.ID == h1.ID() && len(pi.Addrs) > 0 {
			targetPeer = &pi
			break
		}
	}
	if targetPeer == nil {
		t.Fatal("FAIL: Could not find Host1 via DHT short code")
	}
	t.Logf("Found Host1 via short code %s: %v", code1, targetPeer.Addrs)

	// --- Set up TransferManagers ---
	// Host1 = receiver (the one being looked up by short code)
	// Host2 = sender  (the one doing the lookup)
	tmpDir := t.TempDir()
	receiveDir := filepath.Join(tmpDir, "received")
	if err := os.MkdirAll(receiveDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tm1 := NewTransferManager(h1, receiveDir) // Host1 receives files
	tm1.Start()

	tm2 := NewTransferManager(h2, tmpDir) // Host2 sends files
	tm2.Start()

	// Track received transfers on Host1
	received := make(chan *Transfer, 8)
	tm1.SetCallbacks(
		func(tr *Transfer) { t.Logf("recv new: %s (%d bytes)", tr.Filename, tr.Size) },
		func(id string, done, total int64) {},
		func(tr *Transfer) { received <- tr },
		func(id, msg string) { t.Logf("recv error: %s: %s", id, msg) },
	)

	// Track sent transfers on Host2
	tm2.SetCallbacks(
		func(tr *Transfer) { t.Logf("send new: %s (%d bytes)", tr.Filename, tr.Size) },
		func(id string, done, total int64) {},
		func(tr *Transfer) {},
		func(id, msg string) { t.Logf("send error: %s: %s", id, msg) },
	)

	// --- Generate test file (1MB random data) ---
	testSize := 1024 * 1024
	testData := make([]byte, testSize)
	if _, err := rand.Read(testData); err != nil {
		t.Fatalf("rand: %v", err)
	}
	testFile := filepath.Join(tmpDir, "testfile.bin")
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	t.Logf("Created test file: %d bytes", testSize)

	// --- Send file from Host2 to Host1 via short code ---
	f, err := os.Open(testFile)
	if err != nil {
		t.Fatalf("open test file: %v", err)
	}
	defer f.Close()

	transfer, doneCh, err := tm2.SendFile(ctx, targetPeer.ID, "testfile.bin", int64(testSize), f)
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	t.Logf("Transfer started: %s", transfer.ID)

	// Wait for send to complete
	select {
	case <-doneCh:
		t.Log("Send completed")
	case <-ctx.Done():
		t.Fatal("Timeout waiting for send")
	}

	// Wait for receive on Host1
	select {
	case r := <-received:
		t.Logf("Receive completed: %s -> %s", r.Filename, r.OutputPath)
		if r.Status != StatusComplete {
			t.Fatalf("Receive status: %s", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for receive")
	}

	// --- Verify file integrity ---
	receivedFile := filepath.Join(receiveDir, "testfile.bin")
	receivedData, err := os.ReadFile(receivedFile)
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}

	if !bytes.Equal(testData, receivedData) {
		t.Fatalf("File mismatch: sent %d bytes, received %d bytes", len(testData), len(receivedData))
	}

	// Check transfer records
	sentTransfer := tm2.GetTransfer(transfer.ID)
	if sentTransfer == nil {
		t.Fatal("Sent transfer not found")
	}
	if sentTransfer.Status != StatusComplete {
		t.Fatalf("Sent transfer status: %s", sentTransfer.Status)
	}
	t.Logf("Sent transfer: %s %d/%d bytes, %.0f B/s", sentTransfer.Filename, sentTransfer.BytesDone, sentTransfer.Size, sentTransfer.Speed)

	t.Log("")
	t.Log("========================================")
	t.Logf("  PASS: File sent via short code %s", code1)
	t.Logf("  Sent:     %d bytes", len(testData))
	t.Logf("  Received: %d bytes", len(receivedData))
	t.Logf("  Integrity: MATCH")
	t.Log("========================================")
}

// TestShortCodeTransferMultipleChunks sends multiple files to stress-test the protocol.
func TestShortCodeTransferMultipleChunks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

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
	t.Logf("Host1: %s  code: %s", h1.ID().ShortString(), code1)
	t.Logf("Host2: %s  code: %s", h2.ID().ShortString(), shortCode(h2.ID()))

	// Connect
	h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), 10*time.Minute)
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// DHT setup
	kdht1, _ := dht.New(ctx, h1, dht.Mode(dht.ModeAutoServer))
	kdht2, _ := dht.New(ctx, h2, dht.Mode(dht.ModeAutoServer))
	defer kdht1.Close()
	defer kdht2.Close()
	kdht1.Bootstrap(ctx)
	kdht2.Bootstrap(ctx)
	time.Sleep(2 * time.Second)

	// Advertise & find
	rd1 := routing.NewRoutingDiscovery(kdht1)
	ns1 := "iswitch-shortcode-" + code1
	rd1.Advertise(ctx, ns1)
	time.Sleep(3 * time.Second)

	rd2 := routing.NewRoutingDiscovery(kdht2)
	findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
	defer findCancel()
	peersCh, _ := rd2.FindPeers(findCtx, ns1)

	var target peer.ID
	for pi := range peersCh {
		if pi.ID == h1.ID() && len(pi.Addrs) > 0 {
			target = pi.ID
			break
		}
	}
	if target == "" {
		t.Fatal("Could not find Host1 via DHT")
	}

	// Transfer managers
	// Host1 = receiver, Host2 = sender
	tmpDir := t.TempDir()
	receiveDir := filepath.Join(tmpDir, "recv")
	os.MkdirAll(receiveDir, 0755)

	tm1 := NewTransferManager(h1, receiveDir) // Host1 receives
	tm1.Start()
	tm2 := NewTransferManager(h2, tmpDir) // Host2 sends
	tm2.Start()

	receivedCount := 0
	doneCh := make(chan struct{})
	totalFiles := 3
	tm1.SetCallbacks(nil, nil, func(tr *Transfer) {
		receivedCount++
		t.Logf("Received %d/%d: %s (%d bytes)", receivedCount, totalFiles, tr.Filename, tr.Size)
		if receivedCount >= totalFiles {
			close(doneCh)
		}
	}, nil)

	// Send 3 files of different sizes from Host2 to Host1
	files := []struct {
		name string
		size int
	}{
		{"small.txt", 100},
		{"medium.bin", 64 * 1024},
		{"large.bin", 1024 * 1024},
	}

	type pending struct {
		name string
		done chan struct{}
		file *os.File
	}
	var pendingTransfers []pending

	for _, f := range files {
		data := make([]byte, f.size)
		rand.Read(data)
		path := filepath.Join(tmpDir, f.name)
		os.WriteFile(path, data, 0644)

		fd, _ := os.Open(path)
		_, doneCh, err := tm2.SendFile(ctx, target, f.name, int64(f.size), fd)
		if err != nil {
			fd.Close()
			t.Fatalf("SendFile %s: %v", f.name, err)
		}
		t.Logf("Sent: %s (%d bytes)", f.name, f.size)
		pendingTransfers = append(pendingTransfers, pending{name: f.name, done: doneCh, file: fd})
	}

	// Wait for all sends to complete, then close files
	for _, p := range pendingTransfers {
		select {
		case <-p.done:
			p.file.Close()
		case <-ctx.Done():
			p.file.Close()
			t.Fatalf("Timeout waiting for send: %s", p.name)
		}
	}

	// Wait for all files received on Host1
	select {
	case <-doneCh:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for all files")
	}

	// Verify all files
	for _, f := range files {
		original, _ := os.ReadFile(filepath.Join(tmpDir, f.name))
		received, err := os.ReadFile(filepath.Join(receiveDir, f.name))
		if err != nil {
			t.Fatalf("Read received %s: %v", f.name, err)
		}
		if !bytes.Equal(original, received) {
			t.Fatalf("Mismatch: %s", f.name)
		}
		t.Logf("Verified: %s (%d bytes) - MATCH", f.name, f.size)
	}

	t.Log("")
	t.Logf("PASS: All %d files sent and verified via short code %s", totalFiles, code1)
}
