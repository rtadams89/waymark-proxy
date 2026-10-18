package healthcheck

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
)

func TestDecodePayload(t *testing.T) {
	cases := []struct {
		in, format string
		want       []byte
		bad        bool
	}{
		{`PING\r\n`, PayloadText, []byte("PING\r\n"), false},
		{`a\x00\x7fb\\`, PayloadText, []byte{'a', 0, 0x7f, 'b', '\\'}, false},
		{`bad\q`, PayloadText, nil, true},
		{`end\`, PayloadText, nil, true},
		{`\x4`, PayloadText, nil, true},
		{"de ad:be ef", PayloadHex, []byte{0xde, 0xad, 0xbe, 0xef}, false},
		{"abc", PayloadHex, nil, true},
		{"zz", PayloadHex, nil, true},
		{strings.Repeat("a", maxPayloadBytes+1), PayloadText, nil, true},
	}
	for _, c := range cases {
		got, err := DecodePayload(c.in, c.format)
		if c.bad != (err != nil) || (!c.bad && !bytes.Equal(got, c.want)) {
			t.Errorf("DecodePayload(%q, %s) = %v, %v", c.in, c.format, got, err)
		}
	}
}

func l4Cfg(typ CheckType, send, expect string) *Config {
	c := DefaultConfig()
	c.ActiveEnabled, c.Type, c.Send, c.Expect = true, typ, send, expect
	c.TimeoutSeconds = 1
	v, err := c.Validate()
	if err != nil {
		panic(err)
	}
	return v
}

func TestTCPAndUDPProbes(t *testing.T) {
	// TCP: banner, then answers PING with PONG
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("220 ready\r\n"))
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				if strings.HasPrefix(string(buf[:n]), "PING") {
					c.Write([]byte("+PONG\r\n"))
				}
			}(c)
		}
	}()
	tcp := Upstream{Addr: ln.Addr().String()}
	ctx := context.Background()
	if r := DefaultProber(ctx, tcp, l4Cfg(CheckTCP, "", "220")); !r.OK {
		t.Fatalf("banner: %+v", r)
	}
	if r := DefaultProber(ctx, tcp, l4Cfg(CheckTCP, `PING\r\n`, "PONG")); !r.OK {
		t.Fatalf("send/expect: %+v", r)
	}
	if r := DefaultProber(ctx, tcp, l4Cfg(CheckTCP, `PING\r\n`, "NOPE")); r.OK {
		t.Fatalf("wrong reply accepted: %+v", r)
	}

	// UDP: echoes upper-cased
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(bytes.ToUpper(buf[:n]), from)
		}
	}()
	udp := Upstream{Addr: pc.LocalAddr().String()}
	if r := DefaultProber(ctx, udp, l4Cfg(CheckUDP, "hello", "HELLO")); !r.OK {
		t.Fatalf("udp: %+v", r)
	}
	if r := DefaultProber(ctx, udp, l4Cfg(CheckUDP, "hello", "")); !r.OK {
		t.Fatalf("udp any reply: %+v", r)
	}
	if r := DefaultProber(ctx, udp, l4Cfg(CheckUDP, "hello", "BYE")); r.OK {
		t.Fatalf("udp wrong reply accepted: %+v", r)
	}
	// Nothing listening: port unreachable or no reply
	dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := dead.LocalAddr().String()
	dead.Close()
	if r := DefaultProber(ctx, Upstream{Addr: addr}, l4Cfg(CheckUDP, "hello", "")); r.OK {
		t.Fatalf("closed UDP port accepted: %+v", r)
	}
	// A UDP check needs something to send
	c := DefaultConfig()
	c.ActiveEnabled, c.Type = true, CheckUDP
	if _, err := c.Validate(); err == nil {
		t.Fatal("UDP check without data accepted")
	}
}
