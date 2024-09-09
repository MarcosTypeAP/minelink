package packet

import (
	"fmt"
	"io"
)

const (
	VERSION       byte = 0
	HeadersLength      = 6
)

type Type byte

const (
	TypeData Type = iota
	TypeClientClosed
	TypeServerClosed
)

var types = map[Type]bool{
	TypeData:         true,
	TypeClientClosed: true,
	TypeServerClosed: true,
}

// Packet = | version = 1 byte | type = 1 byte | payload length = 4 bytes | payload = 'payload length' bytes |
type Packet []byte

func NewEmptyPacket(type_ Type) Packet {
	p := make(Packet, HeadersLength)
	if err := p.SetVersion(VERSION); err != nil {
		panic(err)
	}
	if err := p.SetType(type_); err != nil {
		panic(err)
	}
	if err := p.SetPayloadLength(0); err != nil {
		panic(err)
	}
	return p
}

func (p Packet) checkLength() error {
	if len(p) < HeadersLength {
		return fmt.Errorf("packet not large enough: min size = %d, got %d", HeadersLength, len(p))
	}
	return nil
}

func (p Packet) Version() (uint8, error) {
	if err := p.checkLength(); err != nil {
		return 0, err
	}
	version := p[0]

	if version != VERSION {
		return version, fmt.Errorf("unsuported packet version: current=%d, got %d", VERSION, version)
	}

	return version, nil
}

func (p Packet) SetVersion(version uint8) error {
	if version != VERSION {
		return fmt.Errorf("unsuported packet version: current=%d, got %d", VERSION, version)
	}
	if err := p.checkLength(); err != nil {
		return err
	}
	p[0] = version
	return nil
}

func (p Packet) Type() (Type, error) {
	if err := p.checkLength(); err != nil {
		return 0, err
	}
	type_ := Type(p[1])

	if _, ok := types[type_]; !ok {
		return type_, fmt.Errorf("unsuported type: %d", type_)
	}
	return type_, nil
}

func (p Packet) SetType(type_ Type) error {
	if _, ok := types[type_]; !ok {
		return fmt.Errorf("unsuported type: %d", type_)
	}

	if err := p.checkLength(); err != nil {
		return err
	}

	p[1] = byte(type_)
	return nil
}

func (p Packet) PayloadLength() (uint32, error) {
	if err := p.checkLength(); err != nil {
		return 0, err
	}
	length := uint32(p[2])<<24 + uint32(p[3])<<16 + uint32(p[4])<<8 + uint32(p[5])
	return length, nil
}

func (p Packet) SetPayloadLength(length uint32) error {
	if err := p.checkLength(); err != nil {
		return err
	}

	p[2] = byte(length & 0xff_00_00_00 >> 24)
	p[3] = byte(length & 0xff_00_00 >> 16)
	p[4] = byte(length & 0xff_00 >> 8)
	p[5] = byte(length & 0xff)
	return nil
}

func (p Packet) Payload() ([]byte, error) {
	length, err := p.PayloadLength()
	if err != nil {
		return nil, err
	}
	if len(p)-HeadersLength < int(length) {
		return nil, fmt.Errorf("incomplete payload: payload length = %d, got %d", length, len(p)-HeadersLength)
	}
	return p[HeadersLength : HeadersLength+length], nil
}

func (p Packet) FromReader(type_ Type, r io.Reader) (Packet, error) {
	if len(p) < HeadersLength {
		panic(fmt.Errorf("packet not large enough: need %d, got %d", HeadersLength, len(p)))
	}
	n, readErr := r.Read(p[HeadersLength:])
	if n == 0 && readErr != nil {
		return nil, readErr
	}

	err := p.SetVersion(VERSION)
	if err != nil {
		panic(err)
	}

	err = p.SetType(type_)
	if err != nil {
		panic(err)
	}

	err = p.SetPayloadLength(uint32(n))
	if err != nil {
		panic(err)
	}

	return p[:HeadersLength+n], readErr
}

type Parser struct {
	buf             Packet
	remainder       Packet
	readingAttempts int
}

func (p *Parser) Buf() Packet {
	return p.buf
}
func (p *Parser) Rem() Packet {
	return p.remainder
}

func NewParser(bufSize int) *Parser {
	return &Parser{
		buf: make(Packet, bufSize),
	}
}

// The returned packets are only valid until the next call
func (p *Parser) ParseFrom(r io.Reader) ([]Packet, error) {
	prev := copy(p.buf, p.remainder)
	p.remainder = nil

	readCount, readErr := r.Read(p.buf[prev:])
	if readCount == 0 && readErr != nil {
		return nil, readErr
	}
	readed := p.buf[:prev+readCount]

	var packets []Packet
	for {
		if len(readed) < HeadersLength {
			break
		}
		headers := Packet(readed[:HeadersLength])
		remainder := readed[HeadersLength:]

		_, err := headers.Version()
		if err != nil {
			return packets, err
		}

		_, err = headers.Type()
		if err != nil {
			return packets, err
		}

		payloadLength, err := headers.PayloadLength()
		if err != nil {
			return packets, err
		}

		if len(remainder) < int(payloadLength) {
			break
		}

		packet := readed[:HeadersLength+payloadLength]
		packets = append(packets, packet)

		readed = readed[len(packet):]
	}

	p.remainder = readed

	if packets == nil {
		// large number to ensure full packet reading without stack overflow (max packet size / max read size (~32KiB on my machine))
		if p.readingAttempts > 200 {
			return nil, fmt.Errorf("read attempts exceeded, incoming packet too large")
		}
		p.readingAttempts++
		return p.ParseFrom(r)
	}
	p.readingAttempts = 0
	return packets, readErr
}
