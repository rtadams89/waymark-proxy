package capture

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type parsedPacket struct {
	comment          string
	srcPort, dstPort uint16
	src, dst         net.IP
	flags            byte
	proto            byte
	payload          []byte
}

// readPcapng is a minimal reader for the files this package writes.
func readPcapng(t *testing.T, path string) []parsedPacket {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []parsedPacket
	sawSHB, sawIDB := false, false
	for len(b) >= 12 {
		typ := binary.LittleEndian.Uint32(b[0:])
		total := int(binary.LittleEndian.Uint32(b[4:]))
		if total < 12 || total > len(b) || binary.LittleEndian.Uint32(b[total-4:]) != uint32(total) {
			t.Fatalf("bad block length %d", total)
		}
		body := b[8 : total-4]
		switch typ {
		case blockSHB:
			sawSHB = binary.LittleEndian.Uint32(body) == 0x1A2B3C4D
		case blockIDB:
			sawIDB = binary.LittleEndian.Uint16(body) == linkTypeRaw
		case blockEPB:
			caplen := int(binary.LittleEndian.Uint32(body[12:]))
			data := body[20 : 20+caplen]
			opts := body[20+caplen+pad4(caplen):]
			p := parsedPacket{}
			for len(opts) >= 4 {
				code, l := binary.LittleEndian.Uint16(opts), int(binary.LittleEndian.Uint16(opts[2:]))
				if code == optComment {
					p.comment = string(opts[4 : 4+l])
				}
				if code == optEnd {
					break
				}
				opts = opts[4+l+pad4(l):]
			}
			if data[0]>>4 != 4 {
				t.Fatalf("expected IPv4, got version %d", data[0]>>4)
			}
			if checksum(data[:20], 0) != 0 {
				t.Fatal("bad IPv4 header checksum")
			}
			p.proto = data[9]
			p.src, p.dst = net.IP(data[12:16]), net.IP(data[16:20])
			seg := data[20:]
			p.srcPort, p.dstPort = binary.BigEndian.Uint16(seg), binary.BigEndian.Uint16(seg[2:])
			if checksum(seg, pseudoSum(p.src, p.dst, p.proto, len(seg))) != 0 {
				t.Fatal("bad transport checksum")
			}
			if p.proto == 6 {
				p.flags = seg[13]
				p.payload = seg[int(seg[12]>>4)*4:]
			} else {
				p.payload = seg[8:]
			}
			out = append(out, p)
		}
		b = b[total:]
	}
	if !sawSHB || !sawIDB {
		t.Fatal("missing section or interface block")
	}
	return out
}

// streamBetween joins the TCP payload sent from srcPort to dstPort.
func streamBetween(pkts []parsedPacket, srcPort, dstPort uint16) string {
	var b strings.Builder
	for _, p := range pkts {
		if p.proto == 6 && p.srcPort == srcPort && p.dstPort == dstPort {
			b.Write(p.payload)
		}
	}
	return b.String()
}

func TestHTTPCaptureBothSides(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, time.Hour, func() string { return "Node-1" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Set-Cookie", "session=secret")
		w.Header().Set("X-Upstream", "yes")
		w.Write(append([]byte("echo:"), body...))
	}))
	defer upstream.Close()

	st, err := m.Start(Config{Scope: ScopeAll, Sides: SideBoth, Hosts: []string{"*.example.com"}, DurationSeconds: 60, MaxMB: 5, MaxBodyKB: 1, Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.File, "waymark-node-1-") {
		t.Fatalf("file name %s", st.File)
	}
	if _, err := m.Start(Config{DurationSeconds: 60, MaxMB: 1}); err == nil {
		t.Fatal("second capture started while one runs")
	}

	proxy := func(host string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://"+host+"/x?y=1", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.5:51000"
		req.Header.Set("Authorization", "Bearer topsecret")
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 443}))
		rec := httptest.NewRecorder()
		w, r, done := Begin(rec, req)
		in, _ := io.ReadAll(r.Body)
		out, _ := http.NewRequestWithContext(r.Context(), "POST", upstream.URL+"/x?y=1", bytes.NewReader(in))
		out, captured := UpstreamRequest(out)
		res, err := http.DefaultTransport.RoundTrip(out)
		res = captured(res, err)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
		res.Body.Close()
		if done != nil {
			done()
		}
		return rec
	}
	big := strings.Repeat("A", 3000) // over the 1 KB body limit
	if rec := proxy("app.example.com", big); rec.Body.String() != "echo:"+big {
		t.Fatal("capture changed the proxied response")
	}
	proxy("other.test", "not captured")
	if _, ok := m.Stop("test done"); !ok {
		t.Fatal("stop failed")
	}

	pkts := readPcapng(t, filepath.Join(dir, st.File))
	// Client side: 203.0.113.5:51000 -> 10.0.0.2, shown on port 80
	clientReq := streamBetween(pkts, 51000, 80)
	clientResp := streamBetween(pkts, 80, 51000)
	if !strings.HasPrefix(clientReq, "POST /x?y=1 HTTP/1.1\r\nHost: app.example.com\r\n") {
		t.Fatalf("client request:\n%q", clientReq)
	}
	if !strings.Contains(clientReq, "Authorization: [redacted]") || strings.Contains(clientReq, "topsecret") {
		t.Fatal("authorization not redacted")
	}
	if !strings.Contains(clientReq, "Content-Length: 1024\r\n") || !strings.Contains(clientReq, "first 1024 of 3000 bytes kept") {
		t.Fatalf("body limit not applied:\n%s", clientReq[:300])
	}
	if !strings.HasPrefix(clientResp, "HTTP/1.1 200 OK\r\n") || !strings.Contains(clientResp, "Set-Cookie: [redacted]") || !strings.Contains(clientResp, "X-Upstream: yes") {
		t.Fatalf("client response:\n%q", clientResp)
	}
	if strings.Contains(clientReq+clientResp, "not captured") {
		t.Fatal("request for another host was captured")
	}
	// Upstream side: the real upstream port, plain HTTP
	_, upPortStr, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	var upPort uint16
	for _, ch := range upPortStr {
		upPort = upPort*10 + uint16(ch-'0')
	}
	var upReq string
	for _, p := range pkts {
		if p.dstPort == upPort && len(p.payload) > 0 {
			upReq += string(p.payload)
		}
	}
	if !strings.HasPrefix(upReq, "POST /x?y=1 HTTP/1.1\r\n") {
		t.Fatalf("upstream request:\n%q", upReq)
	}
	// Comments on the first packet of each conversation
	comments := []string{}
	for _, p := range pkts {
		if p.comment != "" {
			comments = append(comments, p.comment)
		}
	}
	if len(comments) != 2 || !strings.Contains(comments[0]+comments[1], "decrypted from TLS on port 443") || !strings.Contains(comments[0]+comments[1], "for client 203.0.113.5") {
		t.Fatalf("comments: %q", comments)
	}
	// Handshake and close: SYN first, FIN present
	if pkts[0].flags != tcpSYN {
		t.Fatalf("first packet flags %x", pkts[0].flags)
	}
	files := m.Files()
	if len(files) != 1 || files[0].Reason != "test done" {
		t.Fatalf("files: %+v", files)
	}
	if err := m.Delete("../" + st.File); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := m.Delete(st.File); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureLimitsAndFilters(t *testing.T) {
	m, _ := NewManager(t.TempDir(), time.Hour, nil, nil)
	bad := []Config{
		{DurationSeconds: 5, MaxMB: 1},
		{DurationSeconds: 60, MaxMB: 0},
		{DurationSeconds: 60, MaxMB: 1, Sides: "sideways"},
		{DurationSeconds: 60, MaxMB: 1, Hosts: []string{"bad host"}},
		{DurationSeconds: 60, MaxMB: 1, Clients: []string{"10.0.0.0/33"}},
		{DurationSeconds: 60, MaxMB: 1, Note: "<script>"},
	}
	for i, c := range bad {
		if _, err := m.Start(c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	_, err := m.Start(Config{Scope: ScopeStream, DurationSeconds: 60, MaxMB: 1, Clients: []string{"192.0.2.0/24"}, StreamRules: []string{"ABC-123"}})
	if err != nil {
		t.Fatal(err)
	}
	c := Active()
	if c.WantsHTTP("x.test", net.ParseIP("192.0.2.1")) {
		t.Fatal("stream-only capture took HTTP")
	}
	if !c.WantsStream("abc-123", net.ParseIP("192.0.2.9")) || c.WantsStream("abc-123", net.ParseIP("198.51.100.1")) || c.WantsStream("other", net.ParseIP("192.0.2.9")) {
		t.Fatal("stream filters")
	}
	// Size limit: writing past 1 MB stops the capture
	f := c.NewTCPFlow(Endpoint{IP: net.ParseIP("192.0.2.9"), Port: 4000}, Endpoint{IP: net.ParseIP("10.0.0.2"), Port: 5432}, "")
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 40 && Active() != nil; i++ {
		f.Send(true, chunk)
		time.Sleep(time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for Active() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if Active() != nil {
		t.Fatal("capture kept running past its size limit")
	}
	files := m.Files()
	if len(files) != 1 || !strings.Contains(files[0].Reason, "size limit") || files[0].Size > 2<<20 {
		t.Fatalf("files: %+v", files)
	}
}

// Custom fork: oversized inputs are cut so every length field stays valid.
func TestOversizedPacketsAreCut(t *testing.T) {
	a := Endpoint{IP: net.ParseIP("10.0.0.1"), Port: 1}
	b := Endpoint{IP: net.ParseIP("10.0.0.2"), Port: 2}
	big := make([]byte, 200000)
	for _, pkt := range [][]byte{udpPacket(a, b, big, 1), tcpPacket(a, b, 1, 1, tcpACK, big, 1)} {
		if len(pkt) > 0xFFFF {
			t.Fatalf("IPv4 packet of %d bytes", len(pkt))
		}
		if got := int(binary.BigEndian.Uint16(pkt[2:])); got != len(pkt) {
			t.Fatalf("total length field %d, packet %d bytes", got, len(pkt))
		}
	}
	var buf bytes.Buffer
	p, err := newPcapng(&buf, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.packet(time.Now(), big, strings.Repeat("x", 100000)); err != nil {
		t.Fatal(err)
	}
}
