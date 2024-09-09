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

type ServerConfig struct {
	SrcAddr    string
	PeerAddr   string
	ServerAddr string

	PeerSyncInterval time.Duration

	DisableCache bool
	UseLocalTime bool
}

type Server struct {
	config ServerConfig

	srcAddr    *net.TCPAddr
	serverAddr net.Addr
	peerAddr   net.Addr

	peerSyncTimer connsync.SyncTimer

	peerState       State
	peerStateUpdate chan State // unbuffered

	logger   *log.Logger
	logLevel LogLevel
}

func NewServer(logger *log.Logger, logLevel LogLevel, config ServerConfig) *Server {
	return &Server{
		config: config,

		peerStateUpdate: make(chan State),

		logger:   logger,
		logLevel: logLevel,
	}
}

func (p *Server) logf(level LogLevel, format string, v ...any) {
	if level > p.logLevel {
		return
	}
	format = fmt.Sprintf("(%s) %s", p.peerAddr, format)
	p.logger.Printf(format, v...)
}

func (p *Server) logServerf(level LogLevel, format string, v ...any) {
	p.logf(level, "rswp:     "+format, v...)
}

func (p *Server) logPeerf(level LogLevel, format string, v ...any) {
	p.logf(level, "rpws: "+format, v...)
}

func (p *Server) setPeerState(state ConnState) {
	p.peerState.ConnState = state
	select {
	case p.peerStateUpdate <- p.peerState:
	default:
	}
}

func (p *Server) PeerState() State {
	return p.peerState
}

func (p *Server) PeerStateUpdate() <-chan State {
	return p.peerStateUpdate
}

func (p *Server) Start(ctx context.Context) error {
	defer func() {
		close(p.peerStateUpdate)
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
			return fmt.Errorf("invalid source address: missing port: %v", p.config.SrcAddr)
		}
		p.srcAddr, err = net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%s", ip, port))
		if err != nil {
			return err
		}
	}

	{
		var err error
		p.peerAddr, err = addrWithDefaultPort(p.config.PeerAddr, DefaultClientPort)
		if err != nil {
			return err
		}
	}

	p.peerState = State{
		Addr:      p.peerAddr,
		ConnState: ConnStateClosed,
	}

	var err error
	p.serverAddr, err = addrWithDefaultPort(p.config.ServerAddr, DefaultMinecraftPort)
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

	serverConn := connsync.NewDialManager(nil, p.serverAddr, connsync.SyncTimer{})
	defer serverConn.Close()

	subCtx, cancelSubCtx := context.WithCancel(ctx)
	defer cancelSubCtx()

	errs := make(chan error, 1)
	defer close(errs)
	go func() {
		errs <- p.readServerWritePeer(subCtx, serverConn, peerConn)
		cancelSubCtx()
	}()
	go func() {
		errs <- p.readPeerWriteServer(subCtx, peerConn, serverConn)
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

func (p *Server) connectPeer(ctx context.Context, conn connsync.ConnManager) error {
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

func (p *Server) readPeer(ctx context.Context, conn connsync.ConnManager, parser *packet.Parser) ([]packet.Packet, error) {
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

func (p *Server) writeServer(ctx context.Context, conn connsync.ConnManager, packets []packet.Packet) error {
	p.logPeerf(LogLevelDebug, "writing server...")
	for i := range packets {
		type_, err := packets[i].Type()
		if err != nil {
			panic(err)
		}

		if type_ == packet.TypeClientClosed {
			p.logPeerf(LogLevelVerbose, "received 'client closed' packet, closing server")
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
				p.logPeerf(LogLevelError, "error writing to server: %v", err)
			}
			return err
		}
		if n != len(payload) {
			panic(fmt.Errorf("could not write entire payload: written = %d, payload size = %d", n, len(payload)))
		}
		p.logPeerf(LogLevelVerbose, "server written")
	}
	return nil
}

func (p *Server) connectServer(ctx context.Context, conn connsync.ConnManager) error {
	p.logPeerf(LogLevelVerbose, "server closed, connecting...")
	err := conn.Connect(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logPeerf(LogLevelError, "error connecting to server: %v", err)
		}
		return err
	}
	p.logPeerf(LogLevelVerbose, "server connected")
	return nil
}

func (p *Server) readPeerWriteServer(ctx context.Context, peerConn, serverConn connsync.ConnManager) error {
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
			p.logPeerf(LogLevelVerbose, "closing server")
			serverConn.Close()
			continue
		}

		for serverConn.IsClosed() && ctx.Err() == nil {
			p.connectServer(ctx, serverConn)
		}
		if ctx.Err() != nil {
			continue
		}

		err = p.writeServer(ctx, serverConn, packets)
		if err != nil {
			if serverConn.IsClosed() {
				continue
			}
			p.logPeerf(LogLevelVerbose, "closing server")
			serverConn.Close()
			p.logPeerf(LogLevelVerbose, "sending 'server closed' packet")
			p.writePeer(ctx, peerConn, packet.NewEmptyPacket(packet.TypeServerClosed))
			continue
		}
	}
	return nil
}

func (p *Server) readServer(ctx context.Context, serverConn connsync.ConnManager, dst packet.Packet) (packet.Packet, error) {
	p.logServerf(LogLevelDebug, "reading server...")
	packet_, err := serverConn.ReadAsPacket(ctx, dst)
	if err != nil {
		if !errors.Is(err, context.Canceled) && err != io.EOF {
			p.logServerf(LogLevelError, "error reading from server: %v", err)
		}
		return nil, err
	}
	p.logServerf(LogLevelVerbose, "server readed")
	return packet_, nil
}

func (p *Server) writePeer(ctx context.Context, conn connsync.ConnManager, b packet.Packet) error {
	p.logServerf(LogLevelDebug, "writing peer...")
	n, err := conn.Write(ctx, b)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.logServerf(LogLevelError, "error writing to peer: %v", err)
		}
		return err
	}
	if n != len(b) {
		return fmt.Errorf("could not write entire payload: written = %d, payload size = %d", n, len(b))
	}
	p.logServerf(LogLevelVerbose, "peer written")
	return nil
}

func (p *Server) readServerWritePeer(ctx context.Context, serverConn, peerConn connsync.ConnManager) error {
	buf := make(packet.Packet, MaxMinecraftPacketSize)

	for ctx.Err() == nil {
		if serverConn.IsClosed() {
			p.logServerf(LogLevelVerbose, "waiting server...")
			serverConn.WaitConnection(ctx)
			if ctx.Err() != nil {
				return nil
			}
			p.logServerf(LogLevelVerbose, "server ready")
		}

		madePacket, err := p.readServer(ctx, serverConn, buf)
		if err != nil {
			if serverConn.IsClosed() {
				continue
			}
			p.logServerf(LogLevelVerbose, "closing server")
			serverConn.Close()
			p.logServerf(LogLevelVerbose, "sending 'server closed' packet")
			p.writePeer(ctx, peerConn, packet.NewEmptyPacket(packet.TypeServerClosed))
			continue
		}

		if peerConn.IsClosed() {
			p.logServerf(LogLevelVerbose, "peer closed, closing server")
			serverConn.Close()
			continue
		}

		err = p.writePeer(ctx, peerConn, madePacket)
		if err != nil {
			p.logServerf(LogLevelVerbose, "closing peer")
			peerConn.Close()
			p.logServerf(LogLevelVerbose, "closing server")
			serverConn.Close()
			continue
		}
	}
	return nil
}
