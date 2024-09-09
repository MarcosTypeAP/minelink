# Minelink

**Minelink** is a Minecraft P2P proxy that allows you to play with your friends outside your LAN without the need for port forwarding on your router.

> This implementation is intended to work with Minecraft under TCP, but can be adapted to work with other games/programs and the protocol they use, or with a little more work, implement some sort of TCP over UDP.

Although it is not guaranteed to work in all cases due to some routers' behavior that change the outgoing port, exposing one that is unknown by everyone. Some routers change them with an offset that can be predicted by having an external server respond with the details of the received connection, but I designed it to be completely independent of any machine that needs routing configuration.

## Installation

### Compile locally

```bash
# Clone the repo
git clone git@github.com:MarcosTypeAP/minelink.git
cd minelink

# Compile
go build -ldflags "-s -w" ./cmd/minelink
./minelink host -help
```

### Compile inside `docker`

```bash
# Clone the repo
git clone git@github.com:MarcosTypeAP/minelink.git
cd minelink

# Compile
docker run --rm -it --mount type=bind,src=$(pwd),dst=/build golang:alpine /bin/sh -c "
    cd /build && \
    addgroup -g $GID user && \
    adduser -D -u $UID G user user && \
    su user -c 'go build -ldflags \"-s -w\" ./cmd/minelink'" 
./minelink host -help
```

If you have errors like `panic: listen tcp4 <address>: bind: address already in use`.
Try enabling `SO_REUSEADDR` setting the environment variable `MINELINK_REUSEADDR` to `1`.
```bash
MINELINK_REUSEADDR=1 minelink host -help
# Or
export MINELINK_REUSEADDR=1
minelink host -help
```

## Usage

Display usage:

```bash
minelink host -help
# Or
minelink join -help
```

### Host

When hosting, you create a new "mini server" for each client that wants to connect to you, so you need to specify a source port for each client. Ports must be in the rage 1024-65536, however, it's recommended to use ports close to 60000 to avoid conflicts with other programs. Also, you'll need to specify the source port for any client that is not using the default one.

> You must share the selected source port with the peer for it to connect.

```c
minelink host -client 60069:123.123.123.10 -client 3333:123.123.123.20:8080
```

> A client's address must be its public address, if not on the same network.

In this example, we are creating two connections:
-  from `<your-private-ip>:60069` to `123.123.123.10:42069` (`42069` is the default client source port)
- from `<your-private-ip>:3333` to `123.123.123.20:8080`

At this point the program attempts to connect to the client via **TCP hole punching**, so for this to work, both peers must to connect to each other at the same time. By default, they synchronize by getting the current time from an **NTP** server and retrying every one second.

> By default, it connects to a Minecraft server at `127.0.0.1:25565`.

#### Minecraft packet route:
```
minecraft client -> proxy client -> INTERNET -> proxy server -> minecraft server
minecraft server -> proxy server -> INTERNET -> proxy client -> minecraft client
```

#### Connection timeline example:
```
                              Host  Client
                               |      |
                      SYN sent |\     |
                               | \    |
                               |  \  /| SYN sent
                               |   \/ |
                               |   /\ |
                               |  /  \| SYN received (connection open)
                               | /   /| ACK,SYN sent
(connection open) SYN received |/   / |                 
                  ACK,SYN sent |\  /  |                  
                                 ...                                      
```

> See `minelink host -help` to configure other parameters

### Join

To join someone's server, you just need to specify the server address.

> The peer server must share its public IP and the source port that it selected for you to connect to.

> A server's address must be its public address, if not on the same network.

```c
minelink join -server 123.123.123.30:60069
```

This is the connection created in this example:
- from `<your-private-ip>:42069` to `123.123.123.30:60069`

Where `42069` is your default source port, and `60069` is the source port the server peer shared with you.

> For more details about the connection process, see [Host](#host).

You can now connect to the server from your Minecraft client by creating a server with the address `127.0.0.1`.

> By default the proxy client starts listening for a Minecraft client at `127.0.0.1:25565`.

> See `minelink join -help` to configure other parameters.

### Example

```bash
# Client 1:
#   - public IP: 123.123.123.10
#   - synchronization interval: 500ms (changed)

# Client 2:
#   - public IP: 123.123.123.20
#   - source port: 8080 (changed)
#   - listen for minecraft client on 127.0.0.1:30000 (changed)

# Server:
#   - public IP: 123.123.123.30
#   - selected port for client 1: 60001
#   - selected port for client 2: 60002
#   - minecraft server address: 127.0.0.1:25000 (changed)
#   - hide logs from client 1

# Server
minelink host -server=127.0.0.1:25000 -client=hide:60001:123.123.123.10#500ms -client=60002:123.123.123.20:8080

# Client 1
minelink join -sync-interval=500ms -server=123.123.123.30:60001

# Client 2
minelink join -listen=127.0.0.1:30000 -src-port=8080 -server=123.123.123.30:60002
```
