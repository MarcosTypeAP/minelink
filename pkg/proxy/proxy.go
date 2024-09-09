package proxy

import (
	"fmt"
	"net"
	"time"
)

const (
	DefaultClientPort uint16 = 42069

	DefaultMinecraftPort uint16 = 25565

	MaxMinecraftPacketSize = 1 << 21

	DefaultPeerSyncInterval = time.Second
)

type State struct {
	Addr      net.Addr
	ConnState ConnState
}

type ConnState int

const (
	ConnStateClosed ConnState = iota
	ConnStateConnecting
	ConnStateConnected
)

func (s ConnState) String() string {
	switch s {
	case ConnStateClosed:
		return "closed"
	case ConnStateConnecting:
		return "connecting"
	case ConnStateConnected:
		return "connected"
	}
	panic("invalid state")
}

type LogLevel int

const (
	// match: level of the new log <= selected level
	LogLevelNone LogLevel = iota
	LogLevelError
	LogLevelWarning
	LogLevelInfo
	LogLevelVerbose
	LogLevelDebug
)

func (l LogLevel) String() string {
	for name, level := range stringToLogLevel {
		if level == l {
			return name
		}
	}
	panic(fmt.Errorf("log level does not exist: %d", l))
}

var stringToLogLevel = map[string]LogLevel{
	"error":   LogLevelError,
	"warning": LogLevelWarning,
	"info":    LogLevelInfo,
	"verbose": LogLevelVerbose,
	"debug":   LogLevelDebug,
	"none":    LogLevelNone,
}

func ParseLogLevel(value string) (LogLevel, error) {
	level, ok := stringToLogLevel[value]
	if !ok {
		return -1, fmt.Errorf("invalid log level: %v", value)
	}
	return level, nil
}

func LogLevelNames() []string {
	names := make([]string, len(stringToLogLevel))
	for name := range stringToLogLevel {
		names = append(names, name)
	}
	return names
}

func addrWithDefaultPort(addr string, defaultPort uint16) (*net.TCPAddr, error) {
	ip, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if len(port) == 0 {
		addr = fmt.Sprintf("%s:%d", ip, defaultPort)
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return nil, err
	}
	return tcpAddr, nil
}
