package p2p

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const ProtocolID = "/iswitch/transfer/1.0.0"

var bufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 65536)
		return &buf
	},
}

type TransferStatus string

const (
	StatusPending      TransferStatus = "pending"
	StatusTransferring TransferStatus = "transferring"
	StatusComplete     TransferStatus = "complete"
	StatusFailed       TransferStatus = "failed"
)

type Transfer struct {
	ID        string         `json:"id"`
	Filename  string         `json:"filename"`
	Size      int64          `json:"size"`
	BytesDone int64          `json:"bytes_done"`
	Status    TransferStatus `json:"status"`
	Direction string         `json:"direction"`
	PeerID    string         `json:"peer_id"`
	Speed     float64        `json:"speed"`
	OutputPath string        `json:"output_path,omitempty"`
}

type TransferManager struct {
	host       host.Host
	dataDir    string
	mu         sync.RWMutex
	transfers  map[string]*Transfer
	onNew      func(*Transfer)
	onProgress func(string, int64, int64)
	onComplete func(*Transfer)
	onError    func(string, string)
}

func NewTransferManager(h host.Host, dataDir string) *TransferManager {
	return &TransferManager{
		host:      h,
		dataDir:   dataDir,
		transfers: make(map[string]*Transfer),
	}
}

func (tm *TransferManager) Start() {
	tm.host.SetStreamHandler(ProtocolID, tm.handleStream)
}

func (tm *TransferManager) SetCallbacks(
	onNew func(*Transfer),
	onProgress func(string, int64, int64),
	onComplete func(*Transfer),
	onError func(string, string),
) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.onNew = onNew
	tm.onProgress = onProgress
	tm.onComplete = onComplete
	tm.onError = onError
}

func (tm *TransferManager) GetTransfer(id string) *Transfer {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t := tm.transfers[id]
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func (tm *TransferManager) ListTransfers() []*Transfer {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	result := make([]*Transfer, 0, len(tm.transfers))
	for _, t := range tm.transfers {
		c := *t
		result = append(result, &c)
	}
	return result
}

func (tm *TransferManager) SendFile(ctx context.Context, pid peer.ID, filename string, size int64, reader io.Reader) (*Transfer, chan struct{}, error) {
	transferID := uuid.New().String()
	filename = filepath.Base(filename)

	t := &Transfer{
		ID:        transferID,
		Filename:  filename,
		Size:      size,
		Status:    StatusPending,
		Direction: "send",
		PeerID:    pid.String(),
	}

	done := make(chan struct{})

	tm.mu.Lock()
	tm.transfers[transferID] = t
	onNew := tm.onNew
	tm.mu.Unlock()

	if onNew != nil {
		onNew(t)
	}

	stream, err := tm.host.NewStream(ctx, pid, ProtocolID)
	if err != nil {
		tm.setError(transferID, fmt.Sprintf("open stream: %v", err))
		close(done)
		return nil, done, fmt.Errorf("open stream: %w", err)
	}

	go func() {
		tm.sendData(transferID, stream, filename, size, reader)
		close(done)
	}()
	return t, done, nil
}

func (tm *TransferManager) sendData(transferID string, stream network.Stream, filename string, size int64, reader io.Reader) {
	defer stream.Close()

	tm.mu.Lock()
	t, ok := tm.transfers[transferID]
	tm.mu.Unlock()
	if !ok {
		return
	}

	t.Status = StatusTransferring
	nameBytes := []byte(filename)
	header := make([]byte, 4+8)
	binary.BigEndian.PutUint32(header[:4], uint32(len(nameBytes)))
	binary.BigEndian.PutUint64(header[4:], uint64(size))

	if _, err := stream.Write(header); err != nil {
		tm.setError(transferID, fmt.Sprintf("write header: %v", err))
		return
	}
	if _, err := stream.Write(nameBytes); err != nil {
		tm.setError(transferID, fmt.Sprintf("write name: %v", err))
		return
	}

	bufPtr := bufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer bufferPool.Put(bufPtr)

	var done int64
	startTime := time.Now()

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, werr := stream.Write(buf[:n]); werr != nil {
				tm.setError(transferID, fmt.Sprintf("write data: %v", werr))
				return
			}
			done += int64(n)
			tm.updateProgress(transferID, done, size, startTime)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			tm.setError(transferID, fmt.Sprintf("read source: %v", err))
			return
		}
	}

	tm.completeTransfer(transferID)
}

func (tm *TransferManager) handleStream(stream network.Stream) {
	pid := stream.Conn().RemotePeer()

	header := make([]byte, 12)
	if _, err := io.ReadFull(stream, header); err != nil {
		log.Printf("read header error from %s: %v", pid.ShortString(), err)
		stream.Reset()
		return
	}

	nameLen := binary.BigEndian.Uint32(header[:4])
	fileSize := int64(binary.BigEndian.Uint64(header[4:]))

	if nameLen > 1024 {
		log.Printf("invalid name length from %s: %d", pid.ShortString(), nameLen)
		stream.Reset()
		return
	}
	if fileSize < 0 {
		log.Printf("invalid file size from %s: %d", pid.ShortString(), fileSize)
		stream.Reset()
		return
	}

	nameBytes := make([]byte, nameLen)
	if _, err := io.ReadFull(stream, nameBytes); err != nil {
		log.Printf("read name error from %s: %v", pid.ShortString(), err)
		stream.Reset()
		return
	}
	filename := filepath.Base(string(nameBytes))

	transferID := uuid.New().String()
	t := &Transfer{
		ID:        transferID,
		Filename:  filename,
		Size:      fileSize,
		Status:    StatusTransferring,
		Direction: "receive",
		PeerID:    pid.String(),
	}

	tm.mu.Lock()
	tm.transfers[transferID] = t
	onNew := tm.onNew
	tm.mu.Unlock()

	if onNew != nil {
		onNew(t)
	}

	outputPath := filepath.Join(tm.dataDir, filename)
	f, err := os.Create(outputPath)
	if err != nil {
		tm.setError(transferID, fmt.Sprintf("create file: %v", err))
		stream.Reset()
		return
	}
	defer f.Close()

	t.OutputPath = outputPath
	var done int64
	bufPtr := bufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer bufferPool.Put(bufPtr)

	startTime := time.Now()

	for done < fileSize {
		maxRead := fileSize - done
		if maxRead > int64(len(buf)) {
			maxRead = int64(len(buf))
		}
		n, err := stream.Read(buf[:maxRead])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				tm.setError(transferID, fmt.Sprintf("write disk: %v", werr))
				stream.Reset()
				return
			}
			done += int64(n)
			tm.updateProgress(transferID, done, fileSize, startTime)
		}
		if err != nil {
			if done < fileSize {
				tm.setError(transferID, "connection closed prematurely")
				stream.Reset()
				return
			}
			break
		}
	}

	tm.completeTransfer(transferID)
	log.Printf("received %s (%d bytes) from %s", filename, fileSize, pid.ShortString())
}

func (tm *TransferManager) updateProgress(id string, done, total int64, startTime time.Time) {
	tm.mu.Lock()
	t, ok := tm.transfers[id]
	if !ok {
		tm.mu.Unlock()
		return
	}
	t.BytesDone = done
	elapsed := time.Since(startTime).Seconds()
	if elapsed > 0 {
		t.Speed = float64(done) / elapsed
	}
	onProgress := tm.onProgress
	tm.mu.Unlock()

	if onProgress != nil {
		onProgress(id, done, total)
	}
}

func (tm *TransferManager) completeTransfer(id string) {
	tm.mu.Lock()
	t, ok := tm.transfers[id]
	if !ok {
		tm.mu.Unlock()
		return
	}
	t.Status = StatusComplete
	onComplete := tm.onComplete
	tm.mu.Unlock()

	if onComplete != nil {
		onComplete(t)
	}
}

func (tm *TransferManager) setError(id string, errMsg string) {
	tm.mu.Lock()
	t, ok := tm.transfers[id]
	if ok {
		t.Status = StatusFailed
	}
	onError := tm.onError
	tm.mu.Unlock()

	if onError != nil {
		onError(id, errMsg)
	}
	log.Printf("transfer %s error: %s", id, errMsg)
}
