package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/multiformats/go-multiaddr"
)

const serviceName = "iswitch"
const dhtNamespace = "iswitch-peers"

type peerEntry struct {
	info     peer.AddrInfo
	lastSeen time.Time
}

type Discovery struct {
	host           host.Host
	svc            mdns.Service
	dht            *dht.IpfsDHT
	wanEnabled     bool
	mu             sync.RWMutex
	peers          map[peer.ID]*peerEntry
	onFound        func(peer.AddrInfo)
	onLost         func(peer.ID)
	onConnected    func(peer.ID)
	onDisconnected func(peer.ID)
	udpConn        *net.UDPConn
}

type discoveryNotifee struct {
	PeerChan chan peer.AddrInfo
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.PeerChan <- pi
}

func NewDiscovery(h host.Host, wanEnabled bool) *Discovery {
	d := &Discovery{
		host:       h,
		wanEnabled: wanEnabled,
		peers:      make(map[peer.ID]*peerEntry),
	}
	h.Network().Notify(&netNotifee{discovery: d})
	return d
}

func (d *Discovery) WANEnabled() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.wanEnabled
}

func (d *Discovery) SetWANEnabled(ctx context.Context, enabled bool) {
	d.mu.Lock()
	d.wanEnabled = enabled
	d.mu.Unlock()

	if enabled {
		go d.startDHT(ctx)
	} else {
		d.mu.Lock()
		if d.dht != nil {
			d.dht.Close()
			d.dht = nil
		}
		d.mu.Unlock()
	}
}

func (d *Discovery) SetCallbacks(onFound func(peer.AddrInfo), onLost func(peer.ID)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onFound = onFound
	d.onLost = onLost
}

func (d *Discovery) SetConnectionCallbacks(onConnected func(peer.ID), onDisconnected func(peer.ID)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onConnected = onConnected
	d.onDisconnected = onDisconnected
}

func (d *Discovery) Start(ctx context.Context) {
	// mDNS for LAN discovery
	ch := make(chan peer.AddrInfo, 32)
	d.svc = mdns.NewMdnsService(d.host, serviceName, &discoveryNotifee{PeerChan: ch})
	if err := d.svc.Start(); err != nil {
		log.Printf("mDNS start error: %v", err)
	}
	go d.run(ctx, ch)

	// Start UDP Broadcast discovery listener
	if err := d.startUDPDiscovery(); err != nil {
		log.Printf("UDP discovery start error: %v", err)
	}

	// DHT for WAN peer discovery (opt-in via -wan flag)
	if d.wanEnabled {
		go d.startDHT(ctx)
	}
}

type DiscoveryMessage struct {
	Type      string   `json:"type"`       // "ping" or "pong"
	PeerID    string   `json:"peer_id"`    // responder's peer ID
	ShortCode string   `json:"short_code"` // responder's short code
	Addrs     []string `json:"addrs"`      // responder's P2P multiaddresses
}

func (d *Discovery) startUDPDiscovery() error {
	addr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:18727")
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.udpConn = conn
	d.mu.Unlock()

	go func() {
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			n, remoteAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var msg DiscoveryMessage
			if err := json.Unmarshal(buf[:n], &msg); err != nil {
				continue
			}
			if msg.Type == "ping" {
				// Reply with our peer info
				myAddrs := d.host.Addrs()
				var addrsStr []string
				for _, a := range myAddrs {
					addrsStr = append(addrsStr, fmt.Sprintf("%s/p2p/%s", a, d.host.ID()))
				}
				reply := DiscoveryMessage{
					Type:      "pong",
					PeerID:    d.host.ID().String(),
					ShortCode: shortCode(d.host.ID()),
					Addrs:     addrsStr,
				}
				replyBytes, _ := json.Marshal(reply)
				conn.WriteToUDP(replyBytes, remoteAddr)
			}
		}
	}()
	return nil
}

func (d *Discovery) ScanSubnet(ctx context.Context, broadcastIP string) ([]peer.AddrInfo, error) {
	laddr, err := net.ResolveUDPAddr("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	raddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:18727", broadcastIP))
	if err != nil {
		return nil, err
	}

	pingMsg := DiscoveryMessage{
		Type: "ping",
	}
	pingBytes, _ := json.Marshal(pingMsg)
	_, err = conn.WriteToUDP(pingBytes, raddr)
	if err != nil {
		return nil, err
	}

	var discovered []peer.AddrInfo
	var mu sync.Mutex

	conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 2048)

	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		var msg DiscoveryMessage
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		if msg.Type == "pong" {
			pid, err := peer.Decode(msg.PeerID)
			if err != nil {
				continue
			}
			var mas []multiaddr.Multiaddr
			for _, aStr := range msg.Addrs {
				ma, err := multiaddr.NewMultiaddr(strings.Split(aStr, "/p2p/")[0])
				if err == nil {
					mas = append(mas, ma)
				}
			}
			pi := peer.AddrInfo{
				ID:    pid,
				Addrs: mas,
			}

			d.addPeer(pi)

			mu.Lock()
			discovered = append(discovered, pi)
			mu.Unlock()
		}
	}

	return discovered, nil
}

func (d *Discovery) startDHT(ctx context.Context) {
	kdht, err := dht.New(ctx, d.host, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		log.Printf("DHT init error: %v", err)
		return
	}
	d.mu.Lock()
	d.dht = kdht
	d.mu.Unlock()

	// Connect to default bootstrap peers concurrently for WAN routing discovery
	var wg sync.WaitGroup
	connectedCount := 0
	var countMu sync.Mutex

	log.Printf("Connecting to DHT bootstrap peers...")
	for _, addr := range dht.DefaultBootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := d.host.Connect(connectCtx, pi); err == nil {
				countMu.Lock()
				connectedCount++
				countMu.Unlock()
			}
		}(*pi)
	}
	wg.Wait()

	if connectedCount == 0 {
		log.Printf("Warning: Failed to connect to any DHT bootstrap peers. WAN connection may be offline.")
	} else {
		log.Printf("DHT bootstrapped successfully. Connected to %d bootstrap peers.", connectedCount)
	}

	if err := kdht.Bootstrap(ctx); err != nil {
		log.Printf("DHT bootstrap error: %v", err)
	}

	routingDiscovery := routing.NewRoutingDiscovery(kdht)
	routingDiscovery.Advertise(ctx, dhtNamespace)
	routingDiscovery.Advertise(ctx, "iswitch-shortcode-"+shortCode(d.host.ID()))

	// Continuous discovery loop
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peersCh, err := routingDiscovery.FindPeers(ctx, dhtNamespace)
			if err != nil {
				log.Printf("DHT find peers error: %v", err)
				continue
			}
			for pi := range peersCh {
				d.addPeer(pi)
			}
		}
	}
}

func (d *Discovery) addPeer(pi peer.AddrInfo) {
	if pi.ID == d.host.ID() {
		return
	}
	// Add addresses to host's peerstore so streams can be opened
	if len(pi.Addrs) > 0 {
		d.host.Peerstore().AddAddrs(pi.ID, pi.Addrs, 10*time.Minute)
	}
	d.mu.Lock()
	existing, ok := d.peers[pi.ID]
	if !ok || !addrInfoEqual(existing.info, pi) {
		d.peers[pi.ID] = &peerEntry{info: pi, lastSeen: time.Now()}
		onFound := d.onFound
		d.mu.Unlock()
		if onFound != nil {
			onFound(pi)
		}
		log.Printf("peer found: %s", pi.ID.ShortString())
	} else {
		existing.lastSeen = time.Now()
		d.mu.Unlock()
	}
}

func (d *Discovery) Stop() {
	if d.svc != nil {
		d.svc.Close()
	}
	if d.dht != nil {
		d.dht.Close()
	}
	d.mu.Lock()
	if d.udpConn != nil {
		d.udpConn.Close()
	}
	d.mu.Unlock()
}

func (d *Discovery) GetPeers() []peer.AddrInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []peer.AddrInfo
	for _, e := range d.peers {
		if time.Since(e.lastSeen) < 30*time.Second {
			result = append(result, e.info)
		}
	}
	return result
}

func (d *Discovery) run(ctx context.Context, ch chan peer.AddrInfo) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case pi := <-ch:
			d.addPeer(pi)
		case <-ticker.C:
			d.cleanupStale()
		}
	}
}

func (d *Discovery) cleanupStale() {
	d.mu.Lock()
	defer d.mu.Unlock()
	
	activePeers := d.host.Network().Peers()
	connectedMap := make(map[peer.ID]bool, len(activePeers))
	for _, p := range activePeers {
		connectedMap[p] = true
	}
	
	now := time.Now()
	for id, e := range d.peers {
		if connectedMap[id] {
			e.lastSeen = now
			continue
		}

		if now.Sub(e.lastSeen) > 30*time.Second {
			if d.onLost != nil {
				d.onLost(id)
			}
			delete(d.peers, id)
			log.Printf("peer lost: %s", id.ShortString())
		}
	}
}

func addrInfoEqual(a, b peer.AddrInfo) bool {
	if a.ID != b.ID {
		return false
	}
	if len(a.Addrs) != len(b.Addrs) {
		return false
	}
	for i := range a.Addrs {
		if a.Addrs[i].String() != b.Addrs[i].String() {
			return false
		}
	}
	return true
}

func shortCode(pid peer.ID) string {
	h := fnv.New32a()
	h.Write([]byte(pid.String()))
	return fmt.Sprintf("%04d", h.Sum32()%10000)
}

func (d *Discovery) FindPeerByShortCode(ctx context.Context, code string) (*peer.AddrInfo, error) {
	d.mu.RLock()
	// First check our active local list
	for _, e := range d.peers {
		if shortCode(e.info.ID) == code {
			d.mu.RUnlock()
			return &e.info, nil
		}
	}
	kdht := d.dht
	d.mu.RUnlock()

	if kdht == nil {
		if !d.wanEnabled {
			// List what we do have for debugging
			var found []string
			for _, e := range d.peers {
				found = append(found, shortCode(e.info.ID))
			}
			if len(found) > 0 {
				return nil, fmt.Errorf("peer with code %s not found on local network (discovered peers: %v)", code, found)
			}
			return nil, fmt.Errorf("peer with code %s not found, no local peers discovered — ensure both devices are on the same WiFi", code)
		}
		return nil, fmt.Errorf("DHT not initialized")
	}

	routingDiscovery := routing.NewRoutingDiscovery(kdht)
	ns := "iswitch-shortcode-" + code

	findCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	peersCh, err := routingDiscovery.FindPeers(findCtx, ns)
	if err != nil {
		return nil, err
	}

	for pi := range peersCh {
		if pi.ID != d.host.ID() && len(pi.Addrs) > 0 {
			// Add to local peerstore
			d.host.Peerstore().AddAddrs(pi.ID, pi.Addrs, 10*time.Minute)
			return &pi, nil
		}
	}

	return nil, fmt.Errorf("peer with code %s not found in DHT", code)
}

type netNotifee struct {
	discovery *Discovery
}

func (n *netNotifee) Listen(network.Network, multiaddr.Multiaddr) {}
func (n *netNotifee) ListenClose(network.Network, multiaddr.Multiaddr) {}
func (n *netNotifee) Connected(net network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	addr := conn.RemoteMultiaddr()
	
	// Enforce single connection: disconnect from any other active peers
	for _, p := range net.Peers() {
		if p != peerID {
			net.ClosePeer(p)
		}
	}

	// Add directly connected peers to discovery immediately so the UI displays them
	n.discovery.addPeer(peer.AddrInfo{
		ID:    peerID,
		Addrs: []multiaddr.Multiaddr{addr},
	})

	n.discovery.mu.Lock()
	onConnected := n.discovery.onConnected
	n.discovery.mu.Unlock()
	if onConnected != nil {
		onConnected(peerID)
	}
}
func (n *netNotifee) Disconnected(net network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	n.discovery.mu.Lock()
	onDisconnected := n.discovery.onDisconnected
	n.discovery.mu.Unlock()
	if onDisconnected != nil {
		onDisconnected(peerID)
	}
}
func (n *netNotifee) OpenedStream(network.Network, network.Stream) {}
func (n *netNotifee) ClosedStream(network.Network, network.Stream) {}
