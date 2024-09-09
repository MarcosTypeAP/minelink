package proxy_test

import (
	"context"
	"github/MarcosTypeAP/minelink/pkg/crossplatform"
	"github/MarcosTypeAP/minelink/pkg/proxy"
	"log"
	"math/rand"
	"net"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

var aLongTimeAgo = time.Unix(1, 0)

func connRead(ctx context.Context, conn *net.TCPConn) ([]byte, error) {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			conn.SetReadDeadline(aLongTimeAgo)
		}
	}()

	conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func connWrite(ctx context.Context, conn *net.TCPConn, b []byte) (int, error) {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			conn.SetWriteDeadline(aLongTimeAgo)
		}
	}()

	conn.SetWriteDeadline(time.Time{})

	n, err := conn.Write(b)
	if err != nil {
		return 0, err
	}
	return n, nil
}

type fakeMinecraftClient struct {
	serverAddr string
	dialer     net.Dialer
	serverConn *net.TCPConn
	closed     bool
}

func newFakeMinecraftClient(serverAddr string) fakeMinecraftClient {
	dialer := net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			var opterr error
			err := c.Control(func(fd uintptr) {
				opterr = crossplatform.SetSocketReuseAddr(fd, true)
			})
			if err != nil {
				return err
			}
			if opterr != nil {
				return opterr
			}
			return nil
		},
	}
	return fakeMinecraftClient{
		dialer:     dialer,
		serverAddr: serverAddr,
	}
}

func (c *fakeMinecraftClient) close() error {
	err := c.serverConn.Close()
	if err != nil {
		return err
	}

	c.closed = true
	return nil
}

func (c *fakeMinecraftClient) connect(ctx context.Context) error {
	conn, err := c.dialer.DialContext(ctx, "tcp4", c.serverAddr)
	if err != nil {
		return err
	}

	c.serverConn = conn.(*net.TCPConn)
	c.closed = false
	return nil
}

func (c *fakeMinecraftClient) read(ctx context.Context) ([]byte, error) {
	return connRead(ctx, c.serverConn)
}

func (c *fakeMinecraftClient) write(ctx context.Context, b []byte) (int, error) {
	return connWrite(ctx, c.serverConn, b)
}

type fakeMinecraftServer struct {
	listenAddr   string
	listenConfig net.ListenConfig
	listener     *net.TCPListener

	clientConn *net.TCPConn
}

func newFakeMinecraftServer(listenAddr string) fakeMinecraftServer {
	listenConfig := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opterr error
			err := c.Control(func(fd uintptr) {
				opterr = crossplatform.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			if err != nil {
				return err
			}
			if opterr != nil {
				return opterr
			}
			return nil
		},
	}

	return fakeMinecraftServer{
		listenConfig: listenConfig,
		listenAddr:   listenAddr,
	}
}

func (s *fakeMinecraftServer) listen() error {
	listener, err := s.listenConfig.Listen(context.Background(), "tcp4", s.listenAddr)
	if err != nil {
		return err
	}

	s.listener = listener.(*net.TCPListener)
	return nil
}

func (s *fakeMinecraftServer) close() error {
	err := s.listener.Close()
	if err != nil {
		return err
	}

	err = s.clientConn.Close()
	if err != nil {
		return err
	}
	return nil
}

func (s *fakeMinecraftServer) accept(ctx context.Context) error {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			s.listener.SetDeadline(aLongTimeAgo)
		}
	}()

	s.listener.SetDeadline(time.Time{})
	conn, err := s.listener.AcceptTCP()
	if err != nil {
		return err
	}

	s.clientConn = conn
	return nil
}

func (s *fakeMinecraftServer) write(ctx context.Context, data []byte) (n int, err error) {
	return connWrite(ctx, s.clientConn, data)
}

func (s *fakeMinecraftServer) read(ctx context.Context) ([]byte, error) {
	return connRead(ctx, s.clientConn)
}

type testingSetup struct {
	mcServerListenAddr  *string
	mcClientConnectAddr *string

	proxyServerSrcAddr *string
	proxyClientSrcAddr *string

	mcClient    *fakeMinecraftClient
	mcServer    *fakeMinecraftServer
	proxyClient *proxy.Client
	proxyServer *proxy.Server

	ctx       context.Context
	cancelCtx context.CancelFunc

	proxyClientCtx       context.Context
	proxyServerCtx       context.Context
	proxyClientCancelCtx context.CancelFunc
	proxyServerCancelCtx context.CancelFunc
}

func testSetupConnections(t *testing.T, proxyClientLogLevel, proxyServerLogLevel proxy.LogLevel) testingSetup {
	t.Setenv("MINELINK_REUSEADDR", "1")
	wg := sync.WaitGroup{}

	ctx, cancelCtx := context.WithCancel(context.Background())
	proxyServerCtx, proxyServerCancelCtx := context.WithCancel(ctx)
	proxyClientCtx, proxyClientCancelCtx := context.WithCancel(ctx)
	t.Cleanup(func() {
		cancelCtx()
		wg.Wait()
	})

	mcServerListenAddr := "127.0.0.69:20000"
	mcClientConnectAddr := "127.0.0.69:30000"

	proxyServerSrcAddr := "127.0.0.69:2000"
	proxyClientSrcAddr := "127.0.0.69:3000"

	proxyServer := proxy.NewServer(log.Default(), proxyServerLogLevel, proxy.ServerConfig{
		SrcAddr:          proxyServerSrcAddr,
		PeerAddr:         proxyClientSrcAddr,
		PeerSyncInterval: 0,
		ServerAddr:       mcServerListenAddr,
		UseLocalTime:     true,
		DisableCache:     true,
	})
	proxyClient := proxy.NewClient(log.Default(), proxyClientLogLevel, proxy.ClientConfig{
		SrcAddr:          proxyClientSrcAddr,
		PeerAddr:         proxyServerSrcAddr,
		PeerSyncInterval: 0,
		ListenAddr:       mcClientConnectAddr,
		UseLocalTime:     true,
		DisableCache:     true,
	})

	mcServer := newFakeMinecraftServer(mcServerListenAddr)
	mcClient := newFakeMinecraftClient(mcClientConnectAddr)

	// start the proxy server
	wg.Add(1)
	go func() {
		defer wg.Done()

		err := proxyServer.Start(proxyServerCtx)
		if err != nil {
			t.Error(err)
		}
		t.Log("proxy server done")
	}()

	// start the proxy client
	wg.Add(1)
	go func() {
		defer wg.Done()

		err := proxyClient.Start(proxyClientCtx)
		if err != nil {
			t.Error(err)
		}
		t.Log("proxy client done")
	}()

	// wait until the proxy client starts listening
	proxyClient.WaitUntilStartsListening(proxyClientCtx)

	// the minecraft client connects to the proxy client
	err := mcClient.connect(ctx)
	if err != nil {
		t.Error(err)
	}
	t.Cleanup(func() {
		err := mcClient.close()
		if err != nil {
			t.Logf("error closing mc client: %v", err)
		}
		t.Log("minecraft client done")
	})

	// send a packet from the minecraft client to the proxy client, so that it re-sends it to the proxy server
	packetFromMcClient := []byte("test packet from client")
	n, err := mcClient.write(ctx, packetFromMcClient)
	if err != nil {
		t.Error(err)
	}
	if n != len(packetFromMcClient) {
		t.Errorf("the mc client could not send the entire packet: %d != %d", n, len(packetFromMcClient))
	}

	// the minecraft server starts listening
	err = mcServer.listen()
	if err != nil {
		t.Error(err)
	}
	t.Cleanup(func() {
		err := mcServer.close()
		if err != nil {
			t.Logf("error closing mc server: %v", err)
		}
		t.Log("minecraft server done")
	})

	// once the proxy server receives the packet, it connects to the minecraft server to send it to it
	err = mcServer.accept(ctx)
	if err != nil {
		t.Error(err)
	}

	// minecraft server receives the packet from the proxy server
	receivedPacket, err := mcServer.read(ctx)
	if err != nil {
		t.Error(err)
	}
	if !slices.Equal(receivedPacket, packetFromMcClient) {
		t.Errorf("the mc server received a packet that does not match with the one sent from mc client: %q != %q", receivedPacket, packetFromMcClient)
	}

	// minecraft server sends a packet to the proxy server
	packetFromMcServerClient := []byte("test packet from server")
	n, err = mcServer.write(ctx, packetFromMcServerClient)
	if err != nil {
		t.Error(err)
	}
	if n != len(packetFromMcServerClient) {
		t.Errorf("the mc server could not send the entire packet: %d != %d", n, len(packetFromMcServerClient))
	}

	// minecraft client receives the packet from the proxy client
	receivedPacket, err = mcClient.read(ctx)
	if err != nil {
		t.Error(err)
	}
	if !slices.Equal(receivedPacket, packetFromMcServerClient) {
		t.Errorf("the mc client received a packet that does not match with the one sent from mc server: %q != %q", receivedPacket, packetFromMcClient)
	}

	return testingSetup{
		ctx:       ctx,
		cancelCtx: cancelCtx,

		proxyServerCtx: proxyServerCtx,
		proxyClientCtx: proxyClientCtx,

		proxyServerCancelCtx: proxyServerCancelCtx,
		proxyClientCancelCtx: proxyClientCancelCtx,

		mcServerListenAddr:  &mcServerListenAddr,
		mcClientConnectAddr: &mcClientConnectAddr,

		proxyClientSrcAddr: &proxyClientSrcAddr,
		proxyServerSrcAddr: &proxyServerSrcAddr,

		proxyServer: proxyServer,
		proxyClient: proxyClient,

		mcServer: &mcServer,
		mcClient: &mcClient,
	}
}

func TestConnectionE2E(t *testing.T) {
	testSetupConnections(t, proxy.LogLevelNone, proxy.LogLevelNone)
}

func TestMinecraftClientReconnection(t *testing.T) {
	s := testSetupConnections(t, proxy.LogLevelNone, proxy.LogLevelNone)
	defer s.cancelCtx()

	err := s.mcClient.close()
	if err != nil {
		t.Errorf("could not close the connection from the mc client to the proxy client: %v", err)
	}

	err = s.mcClient.connect(s.ctx)
	if err != nil {
		t.Errorf("mc client could not connect to the proxy client: %v", err)
	}

	packetFromMcClient := []byte("test packet from mc client")
	_, err = s.mcClient.write(s.ctx, packetFromMcClient)
	if err != nil {
		t.Errorf("mc client could not send the packet: %v", err)
	}

	err = s.mcServer.accept(s.ctx)
	if err != nil {
		t.Error(err)
	}

	receivedData, err := s.mcServer.read(s.ctx)
	if err != nil {
		t.Errorf("mc server could not receive the packet from mc client: %v", err)
	}
	if !slices.Equal(receivedData, packetFromMcClient) {
		t.Errorf("the data received by the mc server is not the same as that sent from mc client: %q != %q", receivedData, packetFromMcClient)
	}
}

func TestProxyClientReconnection(t *testing.T) {
	s := testSetupConnections(t, proxy.LogLevelNone, proxy.LogLevelNone)
	defer s.cancelCtx()

	s.proxyClientCancelCtx()

	for range s.proxyClient.PeerStateUpdate() {
	}

	proxyClientCtx, proxyClientCancelCtx := context.WithCancel(s.ctx)
	_ = proxyClientCancelCtx

	wg := sync.WaitGroup{}
	t.Cleanup(func() {
		s.proxyClientCancelCtx()
		wg.Wait()
	})

	proxyClient := proxy.NewClient(log.Default(), proxy.LogLevelNone, proxy.ClientConfig{
		SrcAddr:          *s.proxyClientSrcAddr,
		PeerAddr:         *s.proxyServerSrcAddr,
		PeerSyncInterval: 0,
		ListenAddr:       *s.mcClientConnectAddr,
		UseLocalTime:     true,
		DisableCache:     true,
	})

	wg.Add(1)
	go func() {
		defer wg.Done()

		err := proxyClient.Start(proxyClientCtx)
		if err != nil {
			t.Errorf("could not start proxy client: %v", err)
		}
		t.Log("proxy client done")
	}()

	proxyClient.WaitUntilStartsListening(proxyClientCtx)

	err := s.mcClient.connect(s.ctx)
	if err != nil {
		t.Errorf("mc client could not connect to proxy client: %v", err)
	}

	packetFromMcClient := []byte("test packet from mc client")
	_, err = s.mcClient.write(s.ctx, packetFromMcClient)
	if err != nil {
		t.Errorf("mc client could not send the packet: %v", err)
	}

	err = s.mcServer.accept(s.ctx)
	if err != nil {
		t.Errorf("mc server could not accept proxy server connection: %v", err)
	}

	receivedData, err := s.mcServer.read(s.ctx)
	if err != nil {
		t.Errorf("mc server could not receive the packet from mc client: %v", err)
	}
	if !slices.Equal(receivedData, packetFromMcClient) {
		t.Errorf("the data received by the mc server is not the same as that sent from mc client: %q != %q", receivedData, packetFromMcClient)
	}
}

func TestConsistencyOfTransmittedDataE2E(t *testing.T) {
	s := testSetupConnections(t, proxy.LogLevelNone, proxy.LogLevelNone)
	defer s.cancelCtx()

	mcServerReceiveFromClient := func(ctx context.Context) ([]byte, error) {
		return s.mcServer.read(ctx)
	}

	mcServerSendToClient := func(ctx context.Context, data []byte) (int, error) {
		return s.mcServer.write(ctx, data)
	}

	t.Run("data consistency from mc client to mc server", func(t *testing.T) {
		testPacketsConsistency(s.ctx, t, 100_000, 10, "minecraft client", s.mcClient.write, "minecraft server", mcServerReceiveFromClient)
		testPacketsConsistency(s.ctx, t, 100, 10_000, "minecraft client", s.mcClient.write, "minecraft server", mcServerReceiveFromClient)
	})

	t.Run("data consistency from mc server to mc client", func(t *testing.T) {
		testPacketsConsistency(s.ctx, t, 100_000, 10, "minecraft server", mcServerSendToClient, "minecraft client ", s.mcClient.read)
		testPacketsConsistency(s.ctx, t, 100, 10_000, "minecraft server", mcServerSendToClient, "minecraft client ", s.mcClient.read)
	})
}

func makeRandomPacket(maxSize int) []byte {
	packet := make([]byte, rand.Intn(maxSize+1))
	for i := range len(packet) {
		packet[i] = byte(rand.Intn(1 << 8))
	}
	return packet
}

func testPacketsConsistency(
	ctx context.Context,
	t *testing.T,
	numberOfPackets int,
	maxPacketSize int,
	senderName string,
	send func(context.Context, []byte) (int, error),
	receiverName string,
	receive func(context.Context) ([]byte, error),
) error {
	wg := sync.WaitGroup{}

	packetsSent := make([][]byte, numberOfPackets)
	totalDataSent := 0
	for i := range numberOfPackets {
		packetsSent[i] = makeRandomPacket(maxPacketSize)
		totalDataSent += len(packetsSent[i])
	}

	dataReceived := make([]byte, 0, totalDataSent)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, packet := range packetsSent {
			n, err := send(ctx, packet)
			if err != nil {
				t.Errorf("%s: sent packet=%d: %v", senderName, i, err)
				return
			}
			if n != len(packet) {
				t.Errorf("%s: sent packet=%d: could not send the entire packet: %d != %d", senderName, i, n, len(packet))
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for len(dataReceived) != totalDataSent {
			packet, err := receive(ctx)
			if err != nil {
				t.Errorf("%s: received packet: %v", receiverName, err)
				return
			}
			dataReceived = slices.Concat(dataReceived, packet)
		}
	}()

	wg.Wait()

	offset := 0
	for _, packet := range packetsSent {
		for i := range len(packet) {
			if dataReceived[offset+i] != packet[i] {
				t.Errorf("%s to %s: the received data is not the same as the data sent", senderName, receiverName)
			}
		}
		offset += len(packet)
	}
	return nil
}
