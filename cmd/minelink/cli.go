package main

import (
	"context"
	"flag"
	"fmt"
	"github/MarcosTypeAP/minelink/pkg/proxy"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cmdClient struct {
	SyncInterval time.Duration
	PeerIP       string
	PeerPort     uint16
	SrcPort      uint16
	HideLogs     bool
}

func (c cmdClient) String() string {
	base := fmt.Sprintf("%d:%s", c.SrcPort, c.PeerIP)
	if c.PeerPort != 0 {
		base = fmt.Sprintf("%s:%d", base, c.PeerPort)
	}
	if c.SyncInterval != proxy.DefaultPeerSyncInterval {
		base = fmt.Sprintf("%s:%s", base, c.SyncInterval)
	}
	return base
}

type cmdClients []cmdClient

func (c cmdClients) String() string {
	return fmt.Sprint([]cmdClient(c))
}

func (c *cmdClients) Set(value string) error {
	if len(value) == 0 {
		return fmt.Errorf("argument required")
	}
	client := cmdClient{}

	value, hide := strings.CutPrefix(value, "hide:")
	if hide {
		client.HideLogs = true
	}

	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid format: %q", value)
	}
	var srcPort cmdPort
	err := srcPort.Set(parts[0])
	if err != nil {
		return fmt.Errorf("invalid source port: %w: %q", err, value)
	}
	client.SrcPort = uint16(srcPort)

	parts = strings.SplitN(parts[1], "#", 2)
	if len(parts) == 2 {
		interval, err := time.ParseDuration(parts[1])
		if err != nil {
			return fmt.Errorf("invalid sync interval: %w: %q", err, value)
		}
		client.SyncInterval = interval
	}

	parts = strings.SplitN(parts[0], ":", 2)
	if len(parts) == 2 {
		var peerPort cmdPort
		err := peerPort.Set(parts[1])
		if err != nil {
			return fmt.Errorf("invalid peer port: %w: %q", err, value)
		}
		client.PeerPort = uint16(peerPort)
	}
	client.PeerIP = parts[0]

	*c = append(*c, client)
	return nil
}

type cmdLogLevel proxy.LogLevel

func (l cmdLogLevel) String() string {
	return fmt.Sprint(proxy.LogLevel(l))
}

func (l *cmdLogLevel) Set(value string) error {
	eventLevel, err := proxy.ParseLogLevel(value)
	if err != nil {
		return err
	}

	*l = cmdLogLevel(eventLevel)
	return nil
}

type cmdPort uint16

func (o cmdPort) String() string {
	return fmt.Sprint(uint16(o))
}

func (o *cmdPort) Set(value string) error {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return fmt.Errorf("must be a number in the range 1024-65536: %v", value)
	}
	*o = cmdPort(port)
	return nil
}

type cmdStrings []string

func (s *cmdStrings) String() string {
	return fmt.Sprint([]string(*s))
}

func (s *cmdStrings) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("missing command: expected 'host' or 'join'")
		os.Exit(1)
	}

	mode := os.Args[1]
	if mode != "host" && mode != "join" {
		fmt.Printf("unknown command (%s): expected 'host' or 'join'\n", mode)
		os.Exit(1)
	}
	args := os.Args[2:]

	cmd := flag.NewFlagSet("minelink "+mode, flag.ExitOnError)

	// common flags
	var cmdLoggerLevel cmdLogLevel = cmdLogLevel(proxy.LogLevelInfo)
	cmd.Var(&cmdLoggerLevel, "log-level", "`level` of logs to print. options: none, info, verbose, debug")
	cmdSrcIP := cmd.String("src-ip", "", "source address `ip`")
	cmdNoCache := cmd.Bool("no-cache", false, "disable cache")
	cmdLocalTimeSync := cmd.Bool("local-time", false, "whether to use local time instead of quering it from an ntp server (disables cache)")

	// host flags
	var cmdHostClients cmdClients
	var cmdHostServerAddr string

	// join flags
	var cmdJoinSrcPort cmdPort = cmdPort(proxy.DefaultClientPort)
	var cmdJoinListenAddr string
	var cmdJoinServerAddr string
	var cmdJoinSyncInterval time.Duration

	switch mode {
	case "host":
		clientDescription := "describes a connection between this host and a `client` (peer). must be specified for each client that will connect\n" +
			"format: [hide:]<src-port>:<client-ip>[:<client-port>][#<sync-interval>]\n" +
			"if 'hide:' is present at the beginning, logs from this client will be hidden\n" +
			fmt.Sprintf("client-port: source port configured by the other peer (default %d)\n", proxy.DefaultClientPort) +
			fmt.Sprintf("sync-interval: time interval to attempt to connect to the client (default %v)", proxy.DefaultPeerSyncInterval)

		cmd.Var(&cmdHostClients, "client", clientDescription)
		cmd.StringVar(&cmdHostServerAddr, "server", fmt.Sprintf("127.0.0.1:%d", proxy.DefaultMinecraftPort), "local server `address`")
	case "join":
		cmd.Var(&cmdJoinSrcPort, "src-port", "source `port`")
		cmd.StringVar(&cmdJoinListenAddr, "listen", fmt.Sprintf("127.0.0.1:%d", proxy.DefaultMinecraftPort), "`address` where the minecraft client connects")
		cmd.StringVar(&cmdJoinServerAddr, "server", "", "server `address` (peer). required")
		cmd.DurationVar(&cmdJoinSyncInterval, "sync-interval", proxy.DefaultPeerSyncInterval, "time interval to attempt to connect to the server")
	}

	cmd.Parse(args)

	switch mode {
	case "host":
		if len(cmdHostClients) == 0 {
			fmt.Fprintln(cmd.Output(), "flag is required: -client")
			cmd.Usage()
			os.Exit(1)
		}
	case "join":
		if cmdJoinServerAddr == "" {
			fmt.Fprintln(cmd.Output(), "flag is required: -server")
			cmd.Usage()
			os.Exit(1)
		}
	}

	if *cmdLocalTimeSync {
		*cmdNoCache = true
	}

	logger := log.New(os.Stdout, "", log.Ltime)

	ctx, releaseCtx := signal.NotifyContext(context.Background(), os.Interrupt)
	defer releaseCtx()
	go func() {
		select {
		case <-ctx.Done():
		}
		releaseCtx()
	}()

	if mode == "host" {
		wg := sync.WaitGroup{}
		for _, client := range cmdHostClients {
			client := client
			wg.Add(1)
			go func() {
				defer wg.Done()

				logLevel := proxy.LogLevel(cmdLoggerLevel)
				if client.HideLogs {
					logLevel = proxy.LogLevelNone
				}

				peerPort := ""
				if client.PeerPort != 0 {
					peerPort = fmt.Sprint(client.PeerPort)
				}

				config := proxy.ServerConfig{
					SrcAddr:          fmt.Sprintf("%s:%d", *cmdSrcIP, client.SrcPort),
					PeerAddr:         fmt.Sprintf("%s:%s", client.PeerIP, peerPort),
					PeerSyncInterval: client.SyncInterval,
					ServerAddr:       cmdHostServerAddr,
					UseLocalTime:     *cmdLocalTimeSync,
					DisableCache:     *cmdNoCache,
				}
				proxyServer := proxy.NewServer(logger, logLevel, config)
				err := proxyServer.Start(ctx)
				if err != nil {
					panic(err)
				}
			}()
		}
		wg.Wait()
	}

	if mode == "join" {
		config := proxy.ClientConfig{
			SrcAddr:          fmt.Sprintf("%s:%d", *cmdSrcIP, cmdJoinSrcPort),
			PeerAddr:         cmdJoinServerAddr,
			PeerSyncInterval: cmdJoinSyncInterval,
			ListenAddr:       cmdJoinListenAddr,
			UseLocalTime:     *cmdLocalTimeSync,
			DisableCache:     *cmdNoCache,
		}
		proxyClient := proxy.NewClient(logger, proxy.LogLevel(cmdLoggerLevel), config)
		err := proxyClient.Start(ctx)
		if err != nil {
			panic(err)
		}
	}
}
