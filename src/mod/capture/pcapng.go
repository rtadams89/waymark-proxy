package capture

/*
	pcapng.go: a minimal pcapng writer and packet builder.

	Waymark does not capture packets from the network. It records the bytes
	it reads and writes on its connections, and this file turns them into
	IPv4/IPv6 packets with TCP or UDP headers (link type RAW), so the file
	opens in Wireshark like an ordinary capture. Sequence numbers, the
	handshake and the closing FINs are made up to match the data; window
	sizes, retransmissions and timing below the application are not real.
*/

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

const (
	linkTypeRaw = 101 // LINKTYPE_RAW: packets start with an IPv4 or IPv6 header

	blockSHB = 0x0A0D0D0A
	blockIDB = 0x00000001
	blockEPB = 0x00000006

	optEnd     = 0
	optComment = 1

	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpPSH = 0x08
	tcpACK = 0x10
)

type pcapWriter struct {
	w io.Writer
	n int64 // bytes written
}

func pad4(n int) int { return (4 - n%4) % 4 }

// Size limits (custom fork). Every length written into a pcapng block or an
// IP, TCP or UDP header has to fit its field, so inputs are cut to these
// limits before anything is allocated. Capture code stays far below them
// (TCP data is split into segmentSize pieces, UDP reads at most 1500 bytes),
// so they only guard against a future caller passing something huge.
const (
	maxOptionValue  = 0xFFFF - 3  // option length is a uint16
	maxComment      = 4096        // packet comments are one line of text
	maxIPPayload    = 0xFFFF - 40 // IPv4 total length / IPv6 payload length are uint16
	maxTCPPayload   = maxIPPayload - 20
	maxUDPPayload   = maxIPPayload - 8
	maxPacketData   = maxIPPayload + 40
	maxBlockBodyLen = 1 << 20
)

// option encodes one pcapng option.
func option(code uint16, value []byte) []byte {
	if len(value) > maxOptionValue {
		value = value[:maxOptionValue]
	}
	b := make([]byte, 4+len(value)+pad4(len(value)))
	binary.LittleEndian.PutUint16(b[0:], code)
	binary.LittleEndian.PutUint16(b[2:], uint16(len(value)))
	copy(b[4:], value)
	return b
}

func endOfOptions() []byte { return []byte{0, 0, 0, 0} }

func (p *pcapWriter) block(typ uint32, body []byte) error {
	if len(body) > maxBlockBodyLen {
		return errors.New("pcapng block too large")
	}
	total := 12 + len(body)
	b := make([]byte, 0, total)
	b = binary.LittleEndian.AppendUint32(b, typ)
	b = binary.LittleEndian.AppendUint32(b, uint32(total))
	b = append(b, body...)
	b = binary.LittleEndian.AppendUint32(b, uint32(total))
	n, err := p.w.Write(b)
	p.n += int64(n)
	return err
}

// newPcapng writes the section header and the one interface description.
func newPcapng(w io.Writer, app string, ifaceDesc string) (*pcapWriter, error) {
	p := &pcapWriter{w: w}
	shb := make([]byte, 0, 64)
	shb = binary.LittleEndian.AppendUint32(shb, 0x1A2B3C4D) // byte-order magic
	shb = binary.LittleEndian.AppendUint16(shb, 1)          // major
	shb = binary.LittleEndian.AppendUint16(shb, 0)          // minor
	shb = binary.LittleEndian.AppendUint64(shb, 0xFFFFFFFFFFFFFFFF)
	shb = append(shb, option(4, []byte(app))...) // shb_userappl
	shb = append(shb, endOfOptions()...)
	if err := p.block(blockSHB, shb); err != nil {
		return nil, err
	}
	idb := make([]byte, 0, 64)
	idb = binary.LittleEndian.AppendUint16(idb, linkTypeRaw)
	idb = binary.LittleEndian.AppendUint16(idb, 0)
	idb = binary.LittleEndian.AppendUint32(idb, 0)     // snaplen: no limit
	idb = append(idb, option(2, []byte("waymark"))...) // if_name
	idb = append(idb, option(3, []byte(ifaceDesc))...) // if_description
	idb = append(idb, option(9, []byte{6})...)         // if_tsresol: microseconds
	idb = append(idb, endOfOptions()...)
	if err := p.block(blockIDB, idb); err != nil {
		return nil, err
	}
	return p, nil
}

// packet writes one enhanced packet block, with an optional comment.
func (p *pcapWriter) packet(ts time.Time, data []byte, comment string) error {
	us := uint64(ts.UnixMicro())
	if len(data) > maxPacketData {
		data = data[:maxPacketData]
	}
	if len(comment) > maxComment {
		comment = comment[:maxComment]
	}
	body := make([]byte, 0, 64)                      // grows with append (no size arithmetic on input lengths)
	body = binary.LittleEndian.AppendUint32(body, 0) // interface 0
	body = binary.LittleEndian.AppendUint32(body, uint32(us>>32))
	body = binary.LittleEndian.AppendUint32(body, uint32(us))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(data)))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(data)))
	body = append(body, data...)
	body = append(body, make([]byte, pad4(len(data)))...)
	if comment != "" {
		body = append(body, option(optComment, []byte(comment))...)
		body = append(body, endOfOptions()...)
	}
	return p.block(blockEPB, body)
}

/* Packets */

// Endpoint is an IP address and port.
type Endpoint struct {
	IP   net.IP
	Port uint16
}

// EndpointOf converts a net.Addr (TCP, UDP or IP) to an Endpoint.
func EndpointOf(a net.Addr) Endpoint {
	switch v := a.(type) {
	case *net.TCPAddr:
		return Endpoint{IP: v.IP, Port: uint16(v.Port)}
	case *net.UDPAddr:
		return Endpoint{IP: v.IP, Port: uint16(v.Port)}
	case *net.IPAddr:
		return Endpoint{IP: v.IP}
	}
	if a != nil {
		if host, port, err := net.SplitHostPort(a.String()); err == nil {
			ip := net.ParseIP(host)
			var p int
			for _, ch := range port {
				if ch < '0' || ch > '9' {
					p = 0
					break
				}
				p = p*10 + int(ch-'0')
			}
			if ip != nil && p < 65536 {
				return Endpoint{IP: ip, Port: uint16(p)}
			}
		}
	}
	return Endpoint{IP: net.IPv4zero}
}

func (e Endpoint) String() string {
	return net.JoinHostPort(e.IP.String(), itoa(int(e.Port)))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// addrPair returns both addresses in the same family: IPv4 if both are
// IPv4 (or IPv4-mapped), else both as IPv6.
func addrPair(a, b net.IP) (net.IP, net.IP, bool) {
	if a == nil {
		a = net.IPv4zero
	}
	if b == nil {
		b = net.IPv4zero
	}
	if a4, b4 := a.To4(), b.To4(); a4 != nil && b4 != nil {
		return a4, b4, true
	}
	return a.To16(), b.To16(), false
}

func checksum(data []byte, initial uint32) uint16 {
	sum := initial
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func pseudoSum(src, dst net.IP, proto byte, length int) uint32 {
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(b[i])<<8 | uint32(b[i+1])
		}
	}
	add(src)
	add(dst)
	sum += uint32(proto)
	sum += uint32(length)
	return sum
}

// ipPacket wraps a transport segment in an IPv4 or IPv6 header.
func ipPacket(src, dst net.IP, proto byte, segment []byte, id uint16) []byte {
	s, d, v4 := addrPair(src, dst)
	if len(segment) > maxIPPayload {
		segment = segment[:maxIPPayload]
	}
	// Transport checksum over the pseudo header
	csumAt := 16 // TCP
	if proto == 17 {
		csumAt = 6
	}
	binary.BigEndian.PutUint16(segment[csumAt:], 0)
	cs := checksum(segment, pseudoSum(s, d, proto, len(segment)))
	if proto == 17 && cs == 0 {
		cs = 0xffff
	}
	binary.BigEndian.PutUint16(segment[csumAt:], cs)
	if v4 {
		h := make([]byte, 20)
		h[0] = 0x45
		binary.BigEndian.PutUint16(h[2:], uint16(20+len(segment)))
		binary.BigEndian.PutUint16(h[4:], id)
		binary.BigEndian.PutUint16(h[6:], 0x4000) // don't fragment
		h[8] = 64
		h[9] = proto
		copy(h[12:16], s)
		copy(h[16:20], d)
		binary.BigEndian.PutUint16(h[10:], checksum(h, 0))
		return append(h, segment...)
	}
	h := make([]byte, 40)
	h[0] = 0x60
	binary.BigEndian.PutUint16(h[4:], uint16(len(segment)))
	h[6] = proto
	h[7] = 64
	copy(h[8:24], s)
	copy(h[24:40], d)
	return append(h, segment...)
}

func tcpPacket(src, dst Endpoint, seq, ack uint32, flags byte, payload []byte, id uint16) []byte {
	if len(payload) > maxTCPPayload {
		payload = payload[:maxTCPPayload]
	}
	seg := make([]byte, 20)
	binary.BigEndian.PutUint16(seg[0:], src.Port)
	binary.BigEndian.PutUint16(seg[2:], dst.Port)
	binary.BigEndian.PutUint32(seg[4:], seq)
	binary.BigEndian.PutUint32(seg[8:], ack)
	seg[12] = 5 << 4
	seg[13] = flags
	binary.BigEndian.PutUint16(seg[14:], 65535)
	seg = append(seg, payload...)
	return ipPacket(src.IP, dst.IP, 6, seg, id)
}

func udpPacket(src, dst Endpoint, payload []byte, id uint16) []byte {
	if len(payload) > maxUDPPayload {
		payload = payload[:maxUDPPayload]
	}
	seg := make([]byte, 8)
	binary.BigEndian.PutUint16(seg[0:], src.Port)
	binary.BigEndian.PutUint16(seg[2:], dst.Port)
	binary.BigEndian.PutUint16(seg[4:], uint16(8+len(payload)))
	seg = append(seg, payload...)
	return ipPacket(src.IP, dst.IP, 17, seg, id)
}
