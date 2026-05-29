package p2p

import (
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
)

func NewHost(listenPort int) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort),
			"/ip6/::/tcp/0",
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d/ws", listenPort),
			"/ip6/::/tcp/0/ws",
		),
		libp2p.DefaultTransports,
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}
	return h, nil
}
