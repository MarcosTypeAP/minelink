package packet_test

import (
	"bytes"
	"github/MarcosTypeAP/minelink/pkg/proxy/packet"
	"io"
	"math/rand"
	"slices"
	"testing"
	"time"
)

func TestParseStream(t *testing.T) {
	t.Run("single packet", func(t *testing.T) {
		packet1 := packet.Packet{
			// version
			packet.VERSION,
			// type
			byte(packet.TypeData),
			// payload length
			0, 0, 0, 3,
			// payload
			1, 2, 3,
		}
		stream := slices.Concat(packet1, []byte{4, 5, 6, 7, 8})
		expected := packet1

		parser := packet.NewParser(len(stream))
		packets, err := parser.ParseFrom(bytes.NewReader(stream))
		if err != nil && err != io.EOF {
			t.Errorf("error parsing stream: %v", err)
		}
		if len(packets) != 1 {
			t.Errorf("empty output")
		}
		if !slices.Equal(packets[0], expected) {
			t.Errorf("packets are not equal:\nwant %v\ngot %v", packets[0], expected)
		}
	})

	t.Run("many packets", func(t *testing.T) {
		packet1 := packet.Packet{
			packet.VERSION,
			byte(packet.TypeData),
			0, 0, 0, 3,
			1, 2, 3,
		}
		packet2 := packet.Packet{
			packet.VERSION,
			byte(packet.TypeData),
			0, 0, 0, 6,
			1, 2, 3, 4, 5, 6,
		}
		packet3 := packet.Packet{
			packet.VERSION,
			byte(packet.TypeData),
			0, 0, 0, 9,
			1, 2, 3, 4, 5, 6, 7, 8, 9,
		}
		stream := slices.Concat(packet1, packet2, packet3, []byte{33, 69, 4, 20})

		expected := []packet.Packet{packet1, packet2, packet3}

		parser := packet.NewParser(len(stream))
		packets, err := parser.ParseFrom(bytes.NewReader(stream))
		if err != nil && err != io.EOF {
			t.Errorf("error parsing stream: %v", err)
		}
		if len(packets) != len(expected) {
			t.Errorf("missing packets: want %d, got %d", len(expected), len(packets))
		}
		for i := range packets {
			if !slices.Equal(packets[i], expected[i]) {
				t.Errorf("packets are not equal:\nwant %v\ngot %v", packets[i], expected[i])
			}
		}
	})

	t.Run("fuzzy parsing", func(t *testing.T) {
		const (
			maxPacketSize   = 1 << 15
			numberOfPackets = 10_000
		)

		var stream bytes.Buffer
		expected := make([]packet.Packet, 0, numberOfPackets)
		for range numberOfPackets {
			p := makeRandomPacket(maxPacketSize)
			expected = append(expected, p)
			stream.Write(p)
		}

		result := make([]packet.Packet, 0, numberOfPackets)
		parser := packet.NewParser(maxPacketSize)
		for {
			packets, err := parser.ParseFrom(&stream)
			if err != nil && err != io.EOF {
				t.Errorf("error parsing stream: %v", err)
			}
			for i := range packets {
				result = append(result, slices.Clone(packets[i]))
			}
			if err == io.EOF {
				break
			}
		}
		if len(result) != len(expected) {
			t.Errorf("missing packets: want %d, got %d", len(expected), len(result))
		}
		for i := range numberOfPackets {
			if !slices.Equal(result[i], expected[i]) {
				res := result[i]
				exp := expected[i]
				if len(res) > 10 {
					res = res[:10]
				}
				if len(exp) > 10 {
					exp = exp[:10]
				}
				t.Errorf("packets are not equal:\nwant %v\ngot %v", exp, res)
			}
		}
	})
	t.Run("packet bigger than buffer", func(t *testing.T) {
		bigPacket := makeRandomPacket(69)

		parser := packet.NewParser(len(bigPacket) - 1)
		packets, err := parser.ParseFrom(bytes.NewReader(bigPacket))
		if err == nil {
			t.Error("should return an error")
		}
		if packets != nil {
			t.Errorf("should not return packets: got %v", packets)
		}
	})
}

func makeRandomPacket(maxSize int) packet.Packet {
	maxSize -= packet.HeadersLength
	size := packet.HeadersLength + rand.Intn(maxSize+1)
	p := make(packet.Packet, size)

	src := rand.NewSource(time.Now().Unix())
	p, err := p.FromReader(packet.TypeData, rand.New(src))
	if err != nil {
		panic(err)
	}
	return p
}
