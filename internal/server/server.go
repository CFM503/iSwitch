package server

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"iswitch/internal/p2p"

	"github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func shortCode(pid peer.ID) string {
	h := fnv.New32a()
	h.Write([]byte(pid.String()))
	return fmt.Sprintf("%04d", h.Sum32()%10000)
}

func encodeConnectionCode(ipStr string, port int, pid peer.ID) string {
	ip := net.ParseIP(ipStr).To4()
	if ip == nil {
		return ""
	}
	pidBytes, err := pid.Marshal()
	if err != nil {
		return ""
	}
	buf := make([]byte, 4+2+len(pidBytes))
	copy(buf[0:4], ip)
	buf[4] = byte(port >> 8)
	buf[5] = byte(port)
	copy(buf[6:], pidBytes)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func decodeConnectionCode(code string) (*peer.AddrInfo, error) {
	buf, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return nil, err
	}
	if len(buf) < 7 {
		return nil, fmt.Errorf("invalid code length")
	}
	ip := net.IPv4(buf[0], buf[1], buf[2], buf[3])
	port := (int(buf[4]) << 8) | int(buf[5])
	pidBytes := buf[6:]
	pid, err := peer.IDFromBytes(pidBytes)
	if err != nil {
		return nil, err
	}
	ma, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", ip.String(), port))
	if err != nil {
		return nil, err
	}
	return &peer.AddrInfo{
		ID:    pid,
		Addrs: []multiaddr.Multiaddr{ma},
	}, nil
}

//go:embed web/*
var webFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	host        host.Host
	discovery   *p2p.Discovery
	transferMgr *p2p.TransferManager
	port        int
	version     string
	http        *http.Server
	clients     map[*websocket.Conn]bool
	register    chan *websocket.Conn
	unregister  chan *websocket.Conn
	broadcast   chan interface{}
	ready       chan struct{}
}

func NewServer(h host.Host, d *p2p.Discovery, tm *p2p.TransferManager, port int, version string) *Server {
	return &Server{
		host:        h,
		discovery:   d,
		transferMgr: tm,
		port:        port,
		version:     version,
		clients:     make(map[*websocket.Conn]bool),
		register:    make(chan *websocket.Conn),
		unregister:  make(chan *websocket.Conn),
		broadcast:   make(chan interface{}, 256),
		ready:       make(chan struct{}),
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/wan", s.handleWAN)
	mux.HandleFunc("/api/peers", s.handlePeers)
	mux.HandleFunc("/api/transfers", s.handleTransfers)
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/download/", s.handleDownload)
	mux.HandleFunc("/api/interfaces", s.handleInterfaces)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/peers/disconnect", s.handleDisconnectPeer)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/ws/upload", s.handleWSUpload)
	webFS, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(webFS)))

	go s.runHub()

	s.discovery.SetCallbacks(
		func(pi peer.AddrInfo) {
		s.broadcast <- map[string]interface{}{
			"type": "peer_found",
			"peer": map[string]interface{}{
				"id":         pi.ID.String(),
				"short_code": shortCode(pi.ID),
				"addrs":      pi.Addrs,
			},
		}
		},
		func(pid peer.ID) {
			s.broadcast <- map[string]interface{}{
				"type":    "peer_lost",
				"peer_id": pid.String(),
			}
		},
	)

	s.discovery.SetConnectionCallbacks(
		func(pid peer.ID) {
			s.broadcast <- map[string]interface{}{
				"type":    "peer_connected",
				"peer_id": pid.String(),
			}
		},
		func(pid peer.ID) {
			s.broadcast <- map[string]interface{}{
				"type":    "peer_disconnected",
				"peer_id": pid.String(),
			}
		},
	)

	s.transferMgr.SetCallbacks(
		func(t *p2p.Transfer) {
			s.broadcast <- map[string]interface{}{
				"type":     "transfer_new",
				"transfer": t,
			}
		},
		func(id string, done, total int64) {
			tr := s.transferMgr.GetTransfer(id)
			if tr != nil {
				s.broadcast <- map[string]interface{}{
					"type":     "transfer_progress",
					"transfer": tr,
				}
			}
		},
		func(t *p2p.Transfer) {
			s.broadcast <- map[string]interface{}{
				"type":     "transfer_complete",
				"transfer": t,
			}
		},
		func(id string, errMsg string) {
			s.broadcast <- map[string]interface{}{
				"type":    "transfer_error",
				"id":      id,
				"message": errMsg,
			}
		},
	)

	for attempt := 0; attempt < 100; attempt++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
		if err == nil {
			s.port = listener.Addr().(*net.TCPAddr).Port
			s.http = &http.Server{Handler: mux}
			close(s.ready)
			log.Printf("web UI: http://localhost:%d", s.port)
			return s.http.Serve(listener)
		}
		if attempt == 0 {
			log.Printf("port %d in use, trying next...", s.port)
		}
		s.port++
	}
	return fmt.Errorf("no free port after 100 attempts")
}

func (s *Server) Port() int {
	return s.port
}

func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

func (s *Server) Stop() {
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.http.Shutdown(ctx)
	}
}

func (s *Server) runHub() {
	for {
		select {
		case conn := <-s.register:
			s.clients[conn] = true
		s.sendJSON(conn, map[string]interface{}{
			"type":       "my_id",
			"id":         s.host.ID().String(),
			"short_code": shortCode(s.host.ID()),
		})
		for _, pi := range s.discovery.GetPeers() {
			s.sendJSON(conn, map[string]interface{}{
				"type": "peer_found",
				"peer": map[string]interface{}{
					"id":         pi.ID.String(),
					"short_code": shortCode(pi.ID),
					"addrs":      pi.Addrs,
				},
			})
			}
			for _, t := range s.transferMgr.ListTransfers() {
				s.sendJSON(conn, map[string]interface{}{
					"type":     "transfer_new",
					"transfer": t,
				})
			}
		case conn := <-s.unregister:
			delete(s.clients, conn)
		case msg := <-s.broadcast:
			for conn := range s.clients {
				s.sendJSON(conn, msg)
			}
		}
	}
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	addrs := make([]string, len(s.host.Addrs()))
	for i, a := range s.host.Addrs() {
		addrs[i] = fmt.Sprintf("%s/p2p/%s", a, s.host.ID())
	}

	connCode := ""
	for _, a := range s.host.Addrs() {
		ipStr, err := a.ValueForProtocol(multiaddr.P_IP4)
		if err == nil && ipStr != "127.0.0.1" {
			portStr, err := a.ValueForProtocol(multiaddr.P_TCP)
			if err == nil {
				var port int
				fmt.Sscanf(portStr, "%d", &port)
				connCode = encodeConnectionCode(ipStr, port, s.host.ID())
				break
			}
		}
	}
	if connCode == "" && len(s.host.Addrs()) > 0 {
		a := s.host.Addrs()[0]
		ipStr, _ := a.ValueForProtocol(multiaddr.P_IP4)
		portStr, _ := a.ValueForProtocol(multiaddr.P_TCP)
		var port int
		fmt.Sscanf(portStr, "%d", &port)
		connCode = encodeConnectionCode(ipStr, port, s.host.ID())
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"peer_id":         s.host.ID().String(),
		"short_code":      shortCode(s.host.ID()),
		"connection_code": connCode,
		"addrs":           addrs,
		"wan_enabled":     s.discovery.WANEnabled(),
		"version":         s.version,
	})
}

var discoveryCtx context.Context
var discoveryCancel context.CancelFunc

func (s *Server) SetDiscoveryContext(ctx context.Context) {
	discoveryCtx = ctx
}

func (s *Server) handleWAN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodPost {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if discoveryCtx != nil {
			s.discovery.SetWANEnabled(discoveryCtx, body.Enabled)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"wan_enabled": s.discovery.WANEnabled(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"wan_enabled": s.discovery.WANEnabled(),
	})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodPost {
		var body struct {
			Addr string `json:"addr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		addrInput := strings.TrimSpace(body.Addr)
		code := strings.TrimPrefix(addrInput, "#")
		isShortCode := false
		if len(code) == 4 {
			isShortCode = true
			for _, c := range code {
				if c < '0' || c > '9' {
					isShortCode = false
					break
				}
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()

		var pi *peer.AddrInfo
		if isShortCode {
			var err error
			pi, err = s.discovery.FindPeerByShortCode(ctx, code)
			if err != nil {
				http.Error(w, fmt.Sprintf("short code lookup failed: %v", err), 404)
				return
			}
		} else if !strings.HasPrefix(addrInput, "/") {
			pid, err := peer.Decode(addrInput)
			if err == nil {
				addrs := s.host.Peerstore().Addrs(pid)
				pi = &peer.AddrInfo{
					ID:    pid,
					Addrs: addrs,
				}
			} else {
				var err2 error
				pi, err2 = decodeConnectionCode(addrInput)
				if err2 != nil {
					http.Error(w, fmt.Sprintf("invalid address or connection code: %v", err2), 400)
					return
				}
			}
		} else {
			ma, err := multiaddr.NewMultiaddr(body.Addr)
			if err != nil {
				http.Error(w, fmt.Sprintf("bad addr: %v", err), 400)
				return
			}
			var pErr error
			pi, pErr = peer.AddrInfoFromP2pAddr(ma)
			if pErr != nil {
				http.Error(w, fmt.Sprintf("bad addr: %v", pErr), 400)
				return
			}
		}

		// Enforce single active connection: close connections to other peers
		for _, p := range s.host.Network().Peers() {
			if p != pi.ID {
				s.host.Network().ClosePeer(p)
			}
		}

		if err := s.host.Connect(ctx, *pi); err != nil {
			http.Error(w, fmt.Sprintf("connect: %v", err), 500)
			return
		}
		w.WriteHeader(200)
		return
	}

	peers := s.discovery.GetPeers()
	result := make([]map[string]interface{}, 0, len(peers))
	for _, p := range peers {
		result = append(result, map[string]interface{}{
			"id":         p.ID.String(),
			"short_code": shortCode(p.ID),
			"addrs":      p.Addrs,
			"connected":  s.host.Network().Connectedness(p.ID) == network.Connected,
		})
	}

	connectedPeers := s.host.Network().Peers()
	connectedList := make([]map[string]interface{}, 0, len(connectedPeers))
	for _, p := range connectedPeers {
		connectedList = append(connectedList, map[string]interface{}{
			"id":         p.String(),
			"short_code": shortCode(p),
			"friendly":   fmt.Sprintf("Device #%s", shortCode(p)),
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"peers":           result,
		"connected_peers": connectedList,
	})
}

func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"transfers": s.transferMgr.ListTransfers(),
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	peerIDStr := r.URL.Query().Get("peer_id")
	if peerIDStr == "" {
		http.Error(w, "peer_id required", 400)
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid peer_id: %v", err), 400)
		return
	}

	r.ParseMultipartForm(32 << 20)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, fmt.Sprintf("file required: %v", err), 400)
		return
	}
	defer file.Close()

	// Stream directly from multipart file — no buffering into memory
	transfer, done, err := s.transferMgr.SendFile(r.Context(), pid, header.Filename, header.Size, file)
	if err != nil {
		http.Error(w, fmt.Sprintf("send failed: %v", err), 500)
		return
	}

	// Wait for the send goroutine to finish reading from `file` before returning
	// (prevents the deferred file.Close() from killing the read mid-stream)
	<-done

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(transfer)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/download/")
	t := s.transferMgr.GetTransfer(id)
	if t == nil {
		http.Error(w, "not found", 404)
		return
	}
	if t.OutputPath == "" {
		http.Error(w, "no file", 404)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, t.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, t.OutputPath)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	s.register <- conn

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() { s.unregister <- conn; conn.Close() }()

	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	<-ctx.Done()
}

func (s *Server) sendJSON(conn *websocket.Conn, v interface{}) {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(v); err != nil {
		log.Printf("ws write error: %v", err)
		conn.Close()
		delete(s.clients, conn)
	}
}

// wsUploadMsg is the JSON header sent before binary chunks on the upload WebSocket.
type wsUploadMsg struct {
	Type     string `json:"type"`
	PeerID   string `json:"peer_id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// wsUploadProgress is sent back to the client with upload progress.
type wsUploadProgress struct {
	Type       string  `json:"type"`
	TransferID string  `json:"transfer_id"`
	Done       int64   `json:"done"`
	Total      int64   `json:"total"`
	Speed      float64 `json:"speed"`
}

// wsUploadResult is sent when upload+transfer completes or fails.
type wsUploadResult struct {
	Type       string `json:"type"`
	TransferID string `json:"transfer_id"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

// handleWSUpload handles WebSocket-based file uploads.
// Protocol: client sends a text message with JSON metadata, then streams binary frames.
func (s *Server) handleWSUpload(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upload upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Read the metadata message (text frame)
	_, metaBytes, err := conn.ReadMessage()
	if err != nil {
		log.Printf("ws upload read meta error: %v", err)
		return
	}

	var meta wsUploadMsg
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		conn.WriteJSON(wsUploadResult{Success: false, Error: "bad metadata: " + err.Error()})
		return
	}

	pid, err := peer.Decode(meta.PeerID)
	if err != nil {
		conn.WriteJSON(wsUploadResult{Success: false, Error: "bad peer_id: " + err.Error()})
		return
	}

	// Create a pipe: writer is fed by WS binary frames, reader is consumed by SendFile
	pr, pw := io.Pipe()

	// Start transfer with the reader end
	transfer, _, err := s.transferMgr.SendFile(r.Context(), pid, meta.Filename, meta.Size, pr)
	if err != nil {
		pw.CloseWithError(err)
		conn.WriteJSON(wsUploadResult{Success: false, Error: "send failed: " + err.Error()})
		return
	}

	// Pump binary frames into the pipe writer in a goroutine
	var done int64
	go func() {
		for {
			msgType, chunk, err := conn.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if msgType == websocket.TextMessage {
				// Check for EOF signal
				var sig struct{ Type string }
				if json.Unmarshal(chunk, &sig) == nil && sig.Type == "eof" {
					pw.Close()
					return
				}
				continue
			}
			if msgType == websocket.BinaryMessage {
				n, werr := pw.Write(chunk)
				done += int64(n)
				if werr != nil {
					pw.CloseWithError(werr)
					return
				}
			}
		}
	}()

	// Monitor progress and send updates back via the control WebSocket
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	transferID := transfer.ID
	timeout := time.After(30 * time.Minute)

	for {
		select {
		case <-ticker.C:
			tr := s.transferMgr.GetTransfer(transferID)
			if tr == nil {
				return
			}
			conn.WriteJSON(wsUploadProgress{
				Type:       "upload_progress",
				TransferID: transferID,
				Done:       tr.BytesDone,
				Total:      tr.Size,
				Speed:      tr.Speed,
			})
			if tr.Status == "complete" {
				conn.WriteJSON(wsUploadResult{Type: "upload_complete", TransferID: transferID, Success: true})
				return
			}
			if tr.Status == "failed" {
				conn.WriteJSON(wsUploadResult{Type: "upload_complete", TransferID: transferID, Success: false, Error: "transfer failed"})
				return
			}
		case <-timeout:
			conn.WriteJSON(wsUploadResult{Type: "upload_complete", TransferID: transferID, Success: false, Error: "timeout"})
			return
		}
	}
}

type NetworkInterface struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Mask      string `json:"mask"`
	Segment   string `json:"segment"`
	Broadcast string `json:"broadcast"`
}

func getLocalInterfaces() []NetworkInterface {
	var list []NetworkInterface
	ifaces, err := net.Interfaces()
	if err == nil && len(ifaces) > 0 {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if !ok || ipNet.IP.IsLoopback() {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil {
					continue
				}
				mask := ipNet.Mask
				ipBytes := []byte(ip4)
				maskBytes := []byte(mask)
				if len(ipBytes) != 4 || len(maskBytes) != 4 {
					continue
				}
				segmentBytes := make([]byte, 4)
				broadcastBytes := make([]byte, 4)
				for i := 0; i < 4; i++ {
					segmentBytes[i] = ipBytes[i] & maskBytes[i]
					broadcastBytes[i] = ipBytes[i] | ^maskBytes[i]
				}
				segmentIP := net.IP(segmentBytes).String()
				broadcastIP := net.IP(broadcastBytes).String()
				ones, _ := mask.Size()

				list = append(list, NetworkInterface{
					Name:      iface.Name,
					IP:        ip4.String(),
					Mask:      net.IP(maskBytes).String(),
					Segment:   fmt.Sprintf("%s/%d", segmentIP, ones),
					Broadcast: broadcastIP,
				})
			}
		}
	}

	// Fallback to net.InterfaceAddrs() if no active interfaces found
	if len(list) == 0 {
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			for idx, addr := range addrs {
				ipNet, ok := addr.(*net.IPNet)
				if !ok || ipNet.IP.IsLoopback() {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil {
					continue
				}
				mask := ipNet.Mask
				ipBytes := []byte(ip4)
				maskBytes := []byte(mask)
				if len(ipBytes) != 4 || len(maskBytes) != 4 {
					continue
				}
				segmentBytes := make([]byte, 4)
				broadcastBytes := make([]byte, 4)
				for i := 0; i < 4; i++ {
					segmentBytes[i] = ipBytes[i] & maskBytes[i]
					broadcastBytes[i] = ipBytes[i] | ^maskBytes[i]
				}
				segmentIP := net.IP(segmentBytes).String()
				broadcastIP := net.IP(broadcastBytes).String()
				ones, _ := mask.Size()

				list = append(list, NetworkInterface{
					Name:      fmt.Sprintf("LAN-%d", idx+1),
					IP:        ip4.String(),
					Mask:      net.IP(maskBytes).String(),
					Segment:   fmt.Sprintf("%s/%d", segmentIP, ones),
					Broadcast: broadcastIP,
				})
			}
		}
	}
	return list
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ifaces := getLocalInterfaces()
	json.NewEncoder(w).Encode(map[string]interface{}{"interfaces": ifaces})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Broadcast string `json:"broadcast"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	broadcastIP := strings.TrimSpace(body.Broadcast)
	if broadcastIP == "" {
		http.Error(w, "broadcast address required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	peers, err := s.discovery.ScanSubnet(ctx, broadcastIP)
	if err != nil {
		http.Error(w, fmt.Sprintf("scan error: %v", err), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, 0, len(peers))
	for _, p := range peers {
		result = append(result, map[string]interface{}{
			"id":         p.ID.String(),
			"short_code": shortCode(p.ID),
			"addrs":      p.Addrs,
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"peers": result})
}

func (s *Server) handleDisconnectPeer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pid, err := peer.Decode(strings.TrimSpace(body.Addr))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid peer ID: %v", err), http.StatusBadRequest)
		return
	}
	
	// Close connection to peer
	if err := s.host.Network().ClosePeer(pid); err != nil {
		http.Error(w, fmt.Sprintf("disconnect failed: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(200)
}
