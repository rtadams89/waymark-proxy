package l4proxy

/*
	capture_custom.go (custom fork)

	Traffic capture for Layer 4 proxies (mod/capture): while a capture that
	covers this rule is running, new TCP connections and UDP client sessions
	are recorded on the client side (client to Waymark's listener), the
	upstream side (Waymark to the upstream server), or both. Connections
	that started before the capture are not recorded.
*/

import (
	"net"
	"sync"

	"imuslab.com/zoraxy/mod/capture"
)

// capConn records what passes through a connection as one TCP flow.
// remoteIsA says whether the remote end opened the connection (the client
// side) or Waymark did (the upstream side).
type capConn struct {
	net.Conn
	flow      *capture.TCPFlow
	remoteIsA bool
}

func (c *capConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.flow.Send(c.remoteIsA, p[:n])
	}
	return n, err
}

func (c *capConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.flow.Send(!c.remoteIsA, p[:n])
	}
	return n, err
}

func (c *capConn) Close() error {
	c.flow.Close()
	return c.Conn.Close()
}

func (c *ProxyRelayInstance) captureComment(side string, upstreamAddr string) string {
	c.poolMu.RLock()
	name := c.Name
	if name == "" {
		name = c.ListeningAddress
	}
	c.poolMu.RUnlock()
	return "Layer 4: " + name + " · " + side + " · destination " + upstreamAddr
}

// captureTCP wraps a new TCP connection pair for the running capture.
func (c *ProxyRelayInstance) captureTCP(client, upstream net.Conn, upstreamAddr string) (net.Conn, net.Conn) {
	cp := capture.Active()
	if cp == nil {
		return client, upstream
	}
	clientEP := capture.EndpointOf(client.RemoteAddr())
	if !cp.WantsL4(c.UUID, clientEP.IP) {
		return client, upstream
	}
	if cp.SideClient() {
		f := cp.NewTCPFlow(clientEP, capture.EndpointOf(client.LocalAddr()), c.captureComment("client to Waymark", upstreamAddr))
		client = &capConn{Conn: client, flow: f, remoteIsA: true}
	}
	if cp.SideUpstream() {
		f := cp.NewTCPFlow(capture.EndpointOf(upstream.LocalAddr()), capture.EndpointOf(upstream.RemoteAddr()), c.captureComment("Waymark to destination, for client "+clientEP.String(), upstreamAddr))
		upstream = &capConn{Conn: upstream, flow: f, remoteIsA: false}
	}
	return client, upstream
}

// udpCapture records one UDP client session.
type udpCapture struct {
	cp                 *capture.Capture
	client, listener   capture.Endpoint
	local, upstreamEP  capture.Endpoint
	clientSide, upSide bool
	mu                 sync.Mutex
	commented          [2]bool
	commentC, commentU string
}

// setupUDPCapture starts recording a new UDP session if a capture wants it.
func (c *ProxyRelayInstance) setupUDPCapture(conn *udpClientServerConn) {
	cp := capture.Active()
	if cp == nil || conn == nil || !cp.WantsL4(c.UUID, conn.ClientAddr.IP) {
		return
	}
	u := &udpCapture{cp: cp, client: capture.EndpointOf(conn.ClientAddr), local: capture.EndpointOf(conn.ServerConn.LocalAddr()),
		upstreamEP: capture.EndpointOf(conn.ServerConn.RemoteAddr()), clientSide: cp.SideClient(), upSide: cp.SideUpstream()}
	if conn.listener != nil {
		u.listener = capture.EndpointOf(conn.listener)
	}
	u.commentC = c.captureComment("client to Waymark (UDP; listener address as configured)", conn.upstream)
	u.commentU = c.captureComment("Waymark to destination (UDP), for client "+u.client.String(), conn.upstream)
	conn.capture = u
}

func (u *udpCapture) comment(i int) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.commented[i] {
		return ""
	}
	u.commented[i] = true
	if i == 0 {
		return u.commentC
	}
	return u.commentU
}

func (u *udpClientServerConn) captureFromClient(p []byte) {
	cp := u.capture
	if cp == nil {
		return
	}
	if cp.clientSide {
		cp.cp.UDP(cp.client, cp.listener, p, cp.comment(0))
	}
	if cp.upSide {
		cp.cp.UDP(cp.local, cp.upstreamEP, p, cp.comment(1))
	}
}

func (u *udpClientServerConn) captureFromUpstream(p []byte) {
	cp := u.capture
	if cp == nil {
		return
	}
	if cp.upSide {
		cp.cp.UDP(cp.upstreamEP, cp.local, p, cp.comment(1))
	}
	if cp.clientSide {
		cp.cp.UDP(cp.listener, cp.client, p, cp.comment(0))
	}
}
