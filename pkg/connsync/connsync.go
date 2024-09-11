package connsync

import (
	"context"
	"fmt"
	"github/MarcosTypeAP/minelink/pkg/common"
	"github/MarcosTypeAP/minelink/pkg/crossplatform"
	"github/MarcosTypeAP/minelink/pkg/proxy/packet"
	"github/MarcosTypeAP/minelink/pkg/storage"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

const syncTimeCacheLifetime = 2 * time.Hour

var (
	aLongTimeAgo = time.Unix(1, 0)

	syncTimeStore      storage.Store[SyncTimeCache]
	syncTimeStoreMutex sync.Mutex
)

func getSyncTimeStore() storage.Store[SyncTimeCache] {
	syncTimeStoreMutex.Lock()
	defer syncTimeStoreMutex.Unlock()

	if syncTimeStore != nil {
		return syncTimeStore
	}

	store, err := storage.NewJsonStore[SyncTimeCache]("sync-time-cache.json", true)
	if err != nil {
		panic(err)
	}
	syncTimeStore = store

	return syncTimeStore
}

type SyncTimeCache struct {
	ExpiresAt time.Time
	Offset    time.Duration
}

type SyncTimer struct {
	offset       time.Duration
	Interval     time.Duration
	DisableCache bool

	store storage.Store[SyncTimeCache]
}

func NewSyncTimer(interval time.Duration, disableCache bool, store storage.Store[SyncTimeCache]) SyncTimer {
	timer := SyncTimer{
		Interval:     interval,
		DisableCache: disableCache,
		store:        store,
	}
	if timer.store == nil {
		timer.store = getSyncTimeStore()
	}
	return timer
}

// Sleeps until it's time to sync
func (t *SyncTimer) Wait() {
	interval := t.Interval.Milliseconds()
	if interval <= 0 {
		return
	}
	now := time.Now().Add(t.offset).UnixMilli()
	timeToSleep := time.Duration(interval-now%interval) * time.Millisecond
	time.Sleep(timeToSleep)
}

func (t *SyncTimer) saveToCache() error {
	data := SyncTimeCache{
		ExpiresAt: time.Now().Add(syncTimeCacheLifetime),
		Offset:    t.offset,
	}
	err := t.store.Save(data)
	if err != nil {
		return err
	}
	return nil
}

func (t *SyncTimer) loadFromCache() (ok bool, err error) {
	data, err := t.store.Load()
	if err != nil {
		return false, err
	}
	if time.Now().After(data.ExpiresAt) {
		return false, nil
	}

	t.offset = data.Offset
	return true, nil
}

func (t *SyncTimer) SyncTime() error {
	if !t.DisableCache {
		ok, err := t.loadFromCache()
		if err != nil {
			log.Printf("sync time: error loading from cache: %v", err)
		}
		if ok {
			if common.DevMode {
				log.Println("sync time: cache hit")
			}
			return nil
		}
		if common.DevMode {
			log.Println("sync time: cache miss")
		}
	}

	// https://datatracker.ietf.org/doc/html/rfc5905
	const (
		ntpPacketSize  = 48
		ntpEpochOffset = 2208988800
	)

	conn, err := net.Dial("udp4", "pool.ntp.org:123")
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	ntpConfigPacket := [ntpPacketSize]byte{0b00_100_011} // no leap year indicator warning, version 4, client mode

	_, err = conn.Write(ntpConfigPacket[:])
	if err != nil {
		return err
	}

	buf := [ntpPacketSize]byte{}
	_, err = conn.Read(buf[:])
	if err != nil {
		return err
	}

	// transmit timestamp
	// uint32 from 32 to 35, big endian
	seconds := int64(buf[35]) | int64(buf[34])<<8 | int64(buf[33])<<16 | int64(buf[32])<<24
	seconds -= ntpEpochOffset
	// uint32 from 36 to 39, big endian
	nanoseconds := int64(buf[39]) | int64(buf[38])<<8 | int64(buf[37])<<16 | int64(buf[36])<<24
	nanoseconds = (nanoseconds * 1e9) >> 32

	t.offset = time.Unix(seconds, nanoseconds).Sub(time.Now())

	if !t.DisableCache {
		err := t.saveToCache()
		if err != nil {
			log.Printf("sync time: error saving to cache: %v", err)
		}
		if common.DevMode {
			log.Println("sync time: saved to cache")
		}
	}

	return nil
}

func isReuseAddrEnabled() bool {
	return os.Getenv("MINELINK_REUSEADDR") == "1"
}

func connManagerReadPackets(ctx context.Context, conn *net.TCPConn, connManager ConnManager, parser *packet.Parser) ([]packet.Packet, error) {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			conn.SetReadDeadline(aLongTimeAgo)
		}
	}()

	err := conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, err
	}

	for ctx.Err() == nil {
		packets, err := parser.ParseFrom(conn)
		if err != nil {
			if connManager.IsClosed() {
				return nil, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return packets, err
	}
	return nil, ctx.Err()
}

func connManagerReadAsPacket(ctx context.Context, conn *net.TCPConn, connManager ConnManager, p packet.Packet) (packet.Packet, error) {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			conn.SetReadDeadline(aLongTimeAgo)
		}
	}()

	err := conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, err
	}

	for ctx.Err() == nil {
		packet, err := p.FromReader(packet.TypeData, conn)
		if err != nil {
			if connManager.IsClosed() {
				return nil, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return packet, err
	}
	return nil, ctx.Err()
}

func connManagerRead(ctx context.Context, conn *net.TCPConn, connManager ConnManager, b []byte) (int, error) {
	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			conn.SetReadDeadline(aLongTimeAgo)
		}
	}()

	err := conn.SetReadDeadline(time.Time{})
	if err != nil {
		return 0, err
	}

	for ctx.Err() == nil {
		n, err := conn.Read(b)
		if err != nil {
			if connManager.IsClosed() {
				return 0, nil
			}
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
		}
		return n, err
	}
	return 0, ctx.Err()
}

func connManagerWrite(ctx context.Context, conn *net.TCPConn, b []byte) (int, error) {
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

	for ctx.Err() == nil {
		n, err := conn.Write(b)
		return n, err
	}
	return 0, ctx.Err()
}

type ConnManager interface {
	Connect(ctx context.Context) error
	Close() error
	IsClosed() bool
	WaitConnection(ctx context.Context)
	Read(ctx context.Context, b []byte) (int, error)
	ReadPackets(ctx context.Context, parser *packet.Parser) ([]packet.Packet, error)
	ReadAsPacket(ctx context.Context, p packet.Packet) (packet.Packet, error)
	Write(ctx context.Context, b []byte) (int, error)
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

type DialManager struct {
	dialer net.Dialer
	conn   *net.TCPConn
	laddr  net.Addr
	raddr  net.Addr

	syncTimer SyncTimer

	closed         bool
	connected      chan struct{} // closed when connected
	connectedMutex sync.RWMutex
	mu             sync.RWMutex
}

func NewDialManager(laddr, raddr net.Addr, syncTimer SyncTimer) *DialManager {
	dialer := net.Dialer{
		LocalAddr: laddr,
		Control: func(network, address string, c syscall.RawConn) error {
			if !isReuseAddrEnabled() {
				return nil
			}
			var opterr error
			err := c.Control(func(fd uintptr) {
				opterr = crossplatform.SetSocketReuseAddr(fd, true)
			})
			if opterr != nil {
				return opterr
			}
			return err
		},
	}
	return &DialManager{
		dialer:    dialer,
		laddr:     laddr,
		raddr:     raddr,
		syncTimer: syncTimer,

		connected: make(chan struct{}),
	}
}

func (m *DialManager) WaitConnection(ctx context.Context) {
Beginning:
	m.connectedMutex.RLock()
	select {
	case <-m.connected:
	case <-ctx.Done():
		m.connectedMutex.RUnlock()
		return
	}
	m.connectedMutex.RUnlock()

	if m.IsClosed() {
		goto Beginning
	}
}

func (m *DialManager) IsClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.conn == nil {
		return true
	}
	return m.closed
}

func (m *DialManager) Close() error {
	if m.IsClosed() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.conn == nil {
		return fmt.Errorf("attempting to close a nil connection")
	}
	err := m.conn.Close()

	select {
	case <-m.connected:
		m.connectedMutex.Lock()
		m.connected = make(chan struct{})
		m.connectedMutex.Unlock()
	default:
	}
	m.closed = true
	return err
}

func (m *DialManager) Connect(ctx context.Context) error {
	if !m.IsClosed() {
		return fmt.Errorf("connection not closed")
	}

	for ctx.Err() == nil {
		m.syncTimer.Wait()

		conn, err := m.dialer.DialContext(ctx, "tcp4", m.raddr.String())
		if err != nil {
			if crossplatform.IsConnError(err) || common.IsConnTimeoutError(err) {
				continue
			}
			return err
		}

		m.Close()
		m.mu.Lock()
		m.conn = conn.(*net.TCPConn)
		m.closed = false
		m.mu.Unlock()
		close(m.connected)
		break
	}
	return ctx.Err()
}

func (m *DialManager) ReadPackets(ctx context.Context, parser *packet.Parser) ([]packet.Packet, error) {
	return connManagerReadPackets(ctx, m.conn, m, parser)
}

func (m *DialManager) ReadAsPacket(ctx context.Context, p packet.Packet) (packet.Packet, error) {
	return connManagerReadAsPacket(ctx, m.conn, m, p)
}

func (m *DialManager) Read(ctx context.Context, b []byte) (int, error) {
	return connManagerRead(ctx, m.conn, m, b)
}

func (m *DialManager) Write(ctx context.Context, b []byte) (int, error) {
	return connManagerWrite(ctx, m.conn, b)
}

func (m *DialManager) LocalAddr() net.Addr {
	return m.laddr
}

func (m *DialManager) RemoteAddr() net.Addr {
	return m.raddr
}

type ListenerManager struct {
	listenConfig net.ListenConfig
	listener     *net.TCPListener
	conn         *net.TCPConn
	listenAddr   net.Addr
	listening    bool

	closed         bool
	connected      chan struct{} // closed when a connection is accepted
	connectedMutex sync.RWMutex
	mu             sync.RWMutex
}

func NewListenerManager(listenAddr net.Addr) *ListenerManager {
	listenConfig := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			if !isReuseAddrEnabled() {
				return nil
			}
			var opterr error
			err := c.Control(func(fd uintptr) {
				opterr = crossplatform.SetSocketReuseAddr(fd, true)
			})
			if opterr != nil {
				return opterr
			}
			return err
		},
	}
	return &ListenerManager{
		listenConfig: listenConfig,
		listenAddr:   listenAddr,

		connected: make(chan struct{}),
	}
}

func (m *ListenerManager) WaitConnection(ctx context.Context) {
Beginning:
	m.connectedMutex.RLock()
	select {
	case <-m.connected:
	case <-ctx.Done():
		m.connectedMutex.RUnlock()
		return
	}
	m.connectedMutex.RUnlock()

	if m.IsClosed() {
		goto Beginning
	}
}

func (m *ListenerManager) IsListening() bool {
	return m.listener != nil && m.listening
}

func (m *ListenerManager) Listen(ctx context.Context) error {
	if m.IsListening() {
		return fmt.Errorf("already listening")
	}

	listener, err := m.listenConfig.Listen(ctx, "tcp4", m.listenAddr.String())
	if err != nil {
		return err
	}

	m.listener = listener.(*net.TCPListener)
	m.listening = true

	return nil
}

func (m *ListenerManager) CloseListener() error {
	if m.listener == nil {
		return fmt.Errorf("attempting to close a nil listener")
	}

	err := m.listener.Close()
	if err != nil {
		return err
	}

	m.listening = false
	return nil
}

func (m *ListenerManager) IsClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.conn == nil {
		return true
	}
	return m.closed
}

func (m *ListenerManager) Close() error {
	if m.IsClosed() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.conn == nil {
		return fmt.Errorf("attempting to close a nil connection")
	}
	err := m.conn.Close()

	select {
	case <-m.connected:
		m.connectedMutex.Lock()
		m.connected = make(chan struct{})
		m.connectedMutex.Unlock()
	default:
	}
	m.closed = true
	return err
}

func (m *ListenerManager) Accept(ctx context.Context) error {
	if !m.IsClosed() {
		return fmt.Errorf("connection not closed")
	}

	if !m.IsListening() {
		return fmt.Errorf("cannot accept connection: not listening")
	}

	cancel := make(chan struct{})
	defer close(cancel)

	go func() {
		select {
		case <-cancel:
		case <-ctx.Done():
			m.listener.SetDeadline(aLongTimeAgo)
		}
	}()

	m.listener.SetDeadline(time.Time{})
	for ctx.Err() == nil {
		conn, err := m.listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		m.Close()
		m.mu.Lock()
		m.conn = conn
		m.closed = false
		m.mu.Unlock()
		close(m.connected)
		break
	}
	return ctx.Err()
}

func (m *ListenerManager) Connect(ctx context.Context) error {
	return m.Accept(ctx)
}

func (m *ListenerManager) ReadPackets(ctx context.Context, parser *packet.Parser) ([]packet.Packet, error) {
	return connManagerReadPackets(ctx, m.conn, m, parser)
}

func (m *ListenerManager) ReadAsPacket(ctx context.Context, p packet.Packet) (packet.Packet, error) {
	return connManagerReadAsPacket(ctx, m.conn, m, p)
}

func (m *ListenerManager) Read(ctx context.Context, b []byte) (int, error) {
	return connManagerRead(ctx, m.conn, m, b)
}

func (m *ListenerManager) Write(ctx context.Context, b []byte) (int, error) {
	return connManagerWrite(ctx, m.conn, b)
}

func (m *ListenerManager) LocalAddr() net.Addr {
	return m.listenAddr
}

func (m *ListenerManager) RemoteAddr() net.Addr {
	if m.conn == nil {
		return nil
	}
	return m.conn.RemoteAddr()
}
