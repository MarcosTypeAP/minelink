package proxy

import (
	"context"
	"errors"
	"fmt"
	"github/MarcosTypeAP/minelink/pkg/common"
	"github/MarcosTypeAP/minelink/pkg/connsync"
	"github/MarcosTypeAP/minelink/pkg/proxy/packet"
	"io"
	"log"
	"net"
	"time"
)

type ClientConfig struct {
	SrcAddr          string
	PeerAddr         string
	PeerSyncInterval time.Duration
	ListenAddr       string

	DisableCache bool
	UseLocalTime bool
}

type Client struct {
	config ClientConfig

	srcAddr       *net.TCPAddr
	peerAddr      *net.TCPAddr
	listenAddr    *net.TCPAddr
	peerSyncTimer connsync.SyncTimer

	peerState       State
	peerStateUpdate chan State // unbuffered

	// send 'true' when it's ready to accept minecraft client connections
	listeningClient chan bool // with buffer size of 1

	logger   *log.Logger
	logLevel LogLevel
}

func NewClient(logger *log.Logger, logLevel LogLevel, config ClientConfig) *Client {
	return &Client{
		config: config,

		peerStateUpdate: make(chan State),

		listeningClient: make(chan bool, 1),

		logger:   logger,
		logLevel: logLevel,
	}
}

func (p *Client) logf(level LogLevel, format string, v ...any) {
	if level > p.logLevel {
		return
	}
	p.logger.Printf(format, v...)
}

func (p *Client) logClientf(level LogLevel, format string, v ...any) {
	p.logf(level, "rcwp:     "+format, v...)
}

func (p *Client) logPeerf(level LogLevel, format string, v ...any) {
	p.logf(level, "rpwc: "+format, v...)
}

func (p *Client) WaitUntilStartsListening(ctx context.Context) bool {
	select {
	case <-ctx.Done():
	case started := <-p.listeningClient:
		return started
	}
	return false
}

func (p *Client) setPeerState(state ConnState) {
	p.peerState.ConnState = state
	select {
	case p.peerStateUpdate <- p.peerState:
	default:
	}
}

func (p *Client) PeerState() State {
	return p.peerState
}

func (p *Client) PeerStateUpdate() <-chan State {
	return p.peerStateUpdate
}

func (p *Client) Start(ctx context.Context) error {
	defer func() {
		close(p.peerStateUpdate)
		close(p.listeningClient)
	}()

	{
		ip, port, err := net.SplitHostPort(p.config.SrcAddr)
		if err != nil {
			return fmt.Errorf("invalid source address: %w", err)
		}
		if len(ip) == 0 {
			ip = common.GetPrivateIP().String()
		}
		if len(port) == 0 {
			port = fmt.Sprint(DefaultClientPort)
		}
		p.srcAddr, err = net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%s", ip, port))
		if err != nil {
			return err
		}
	}

	{
		addr, err := net.ResolveTCPAddr("tcp4", p.config.PeerAddr)
		if err != nil {
			return err
		}
		p.peerAddr = addr
	}

	p.peerState = State{
		Addr:      p.peerAddr,
		ConnState: ConnStateClosed,
	}

	var err error
	p.listenAddr, err = addrWithDefaultPort(p.config.ListenAddr, DefaultMinecraftPort)
	if err != nil {
		return err
	}

	p.peerSyncTimer = connsync.NewSyncTimer(p.config.PeerSyncInterval, p.config.DisableCache, nil)
	if !p.config.UseLocalTime {
		err := p.peerSyncTimer.SyncTime()
		if err != nil {
			return err
		}
	}

	peerConn := connsync.NewDialManager(p.srcAddr, p.peerAddr, p.peerSyncTimer)
	defer peerConn.Close()

	clientConn := connsync.NewListenerManager(p.listenAddr)
	defer clientConn.Close()

	err = clientConn.Listen(ctx)
	if err != nil {
		return err
	}
	defer clientConn.CloseListener()
	p.listeningClient <- true

	subCtx, cancelSubCtx := context.WithCancel(ctx)
	defer cancelSubCtx()

	errs := make(chan error, 1)
	defer close(errs)
	go func() {
		errs <- p.readClientWritePeer(subCtx, clientConn, peerConn)
		cancelSubCtx()
	}()
	go func() {
		errs <- p.readPeerWriteClient(subCtx, peerConn, clientConn)
		cancelSubCtx()
	}()

	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				return err
			}
		}
	}

	p.logf(LogLevelInfo, "gracefully stopped")
	return nil
}

func (p *Client) connectPeer(ctx context.Context, conn connsync.ConnManager) error {
	p.logPeerf(LogLevelInfo, "peer closed, connecting...")
	p.setPeerState(ConnStateConnecting)
	err := conn.Connect(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logPeerf(LogLevelError, "error connecting to peer: %v", err)
		}
		return err
	}
	p.logPeerf(LogLevelInfo, "peer connected")
	p.setPeerState(ConnStateConnected)
	return nil
}

func (p *Client) readPeer(ctx context.Context, conn connsync.ConnManager, parser *packet.Parser) ([]packet.Packet, error) {
	p.logPeerf(LogLevelDebug, "reading peer...")
	packets, err := conn.ReadPackets(ctx, parser)
	if err != nil {
		if !errors.Is(err, context.Canceled) && err != io.EOF {
			p.logPeerf(LogLevelError, "error reading from peer: %v", err)
		}
		return packets, err
	}
	p.logPeerf(LogLevelVerbose, "peer readed")
	return packets, nil
}

func (p *Client) writeClient(ctx context.Context, conn connsync.ConnManager, packets []packet.Packet) error {
	p.logPeerf(LogLevelDebug, "writing client...")
	for i := range packets {
		type_, err := packets[i].Type()
		if err != nil {
			panic(err)
		}

		if type_ == packet.TypeServerClosed {
			p.logPeerf(LogLevelVerbose, "received 'server closed' packet, closing client")
			conn.Close()
			return nil
		}

		payload, err := packets[i].Payload()
		if err != nil {
			panic(err)
		}

		n, err := conn.Write(ctx, payload)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				p.logPeerf(LogLevelError, "error writing to client: %v", err)
			}
			return err
		}
		if n != len(payload) {
			panic(fmt.Errorf("could not write entire payload: written = %d, payload size = %d", n, len(payload)))
		}
	}
	p.logPeerf(LogLevelVerbose, "client written")
	return nil
}

func (p *Client) readPeerWriteClient(ctx context.Context, peerConn, clientConn connsync.ConnManager) error {
	parser := packet.NewParser(MaxMinecraftPacketSize)

	for ctx.Err() == nil {
		if peerConn.IsClosed() {
			err := p.connectPeer(ctx, peerConn)
			if err != nil {
				continue
			}
		}

		packets, err := p.readPeer(ctx, peerConn, parser)
		if err != nil {
			p.logPeerf(LogLevelVerbose, "closing peer")
			peerConn.Close()
			p.logPeerf(LogLevelVerbose, "closing client")
			clientConn.Close()
			continue
		}

		if clientConn.IsClosed() {
			p.logPeerf(LogLevelVerbose, "waiting client...")
			clientConn.WaitConnection(ctx)
			if ctx.Err() != nil {
				return nil
			}
			p.logPeerf(LogLevelVerbose, "client ready")
		}

		err = p.writeClient(ctx, clientConn, packets)
		if err != nil {
			if clientConn.IsClosed() {
				continue
			}
			p.logPeerf(LogLevelVerbose, "closing client")
			clientConn.Close()
			p.logPeerf(LogLevelVerbose, "sending 'client closed' packet")
			p.writePeer(ctx, peerConn, packet.NewEmptyPacket(packet.TypeClientClosed))
			continue
		}
	}
	return nil
}

func (p *Client) connectClient(ctx context.Context, conn connsync.ConnManager) error {
	p.logClientf(LogLevelVerbose, "client closed, connecting...")
	err := conn.Connect(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logClientf(LogLevelError, "error connecting to client: %v", err)
		}
		return err
	}
	p.logClientf(LogLevelVerbose, "client connected")
	return nil
}

func (p *Client) readClient(ctx context.Context, clientConn connsync.ConnManager, dst packet.Packet) (packet.Packet, error) {
	p.logClientf(LogLevelDebug, "reading client...")
	packet_, err := clientConn.ReadAsPacket(ctx, dst)
	if err != nil {
		if !errors.Is(err, context.Canceled) && err != io.EOF {
			p.logClientf(LogLevelError, "error reading from client: %v", err)
		}
		return nil, err
	}
	p.logClientf(LogLevelVerbose, "client readed")
	return packet_, nil
}

func (p *Client) writePeer(ctx context.Context, conn connsync.ConnManager, b packet.Packet) error {
	p.logClientf(LogLevelDebug, "writing peer...")
	n, err := conn.Write(ctx, b)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logClientf(LogLevelError, "error writing to peer: %v", err)
		}
		return err
	}
	if n != len(b) {
		return fmt.Errorf("could not write entire payload: written = %d, payload size = %d", n, len(b))
	}
	p.logClientf(LogLevelVerbose, "peer written")
	return nil
}

func (p *Client) readClientWritePeer(ctx context.Context, clientConn, peerConn connsync.ConnManager) error {
	buf := make(packet.Packet, MaxMinecraftPacketSize)

	for ctx.Err() == nil {
		if peerConn.IsClosed() {
			p.logClientf(LogLevelVerbose, "waiting peer...")
			peerConn.WaitConnection(ctx)
			if ctx.Err() != nil {
				continue
			}
			p.logClientf(LogLevelVerbose, "peer ready")
		}

		if clientConn.IsClosed() {
			err := p.connectClient(ctx, clientConn)
			if err != nil {
				continue
			}
		}

		madePacket, err := p.readClient(ctx, clientConn, buf)
		if err != nil {
			if clientConn.IsClosed() {
				continue
			}
			p.logClientf(LogLevelVerbose, "closing client")
			clientConn.Close()
			p.logClientf(LogLevelVerbose, "sending 'client closed' packet")
			p.writePeer(ctx, peerConn, packet.NewEmptyPacket(packet.TypeClientClosed))
			continue
		}

		if peerConn.IsClosed() {
			p.logClientf(LogLevelVerbose, "peer closed, closing client")
			clientConn.Close()
			continue
		}

		err = p.writePeer(ctx, peerConn, madePacket)
		if err != nil {
			p.logClientf(LogLevelVerbose, "closing peer")
			peerConn.Close()
			p.logClientf(LogLevelVerbose, "closing client")
			clientConn.Close()
			continue
		}
	}
	return nil
}
