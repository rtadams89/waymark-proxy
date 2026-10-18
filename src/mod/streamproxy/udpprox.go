package streamproxy

import (
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

/*
	UDP Proxy Module
*/

// defaultUDPTimeout is the idle-session timeout used for UDP proxy sessions
const defaultUDPTimeout = 60 * time.Second

// Information maintained for each client/server connection
type udpClientServerConn struct {
	ClientAddr *net.UDPAddr  // Address of the client
	ServerConn *net.UDPConn  // UDP connection to server
	timeout    time.Duration // Idle timeout for this session; 0 disables expiry

	// Custom fork: upstream pool and capture (pool_custom.go, capture_custom.go)
	upstream string       // upstream address this session uses
	release  func()       // stops counting the session
	capture  *udpCapture  // non-nil while the session is being captured
	listener *net.UDPAddr // the address the client sent to
}

// Close ends the session (used by CloseOnUpstreamDown).
func (u *udpClientServerConn) Close() error { return u.ServerConn.Close() }

// udpIdleTimeout returns the timeout value of this rule
func (c *ProxyRelayInstance) udpIdleTimeout() time.Duration {
	if c.Timeout > 0 {
		return time.Duration(c.Timeout) * time.Second
	}
	return defaultUDPTimeout
}

// Generate a new connection by opening a UDP connection to the server
func createNewUDPConn(srvAddr, cliAddr *net.UDPAddr, timeout time.Duration) *udpClientServerConn {
	conn := new(udpClientServerConn)
	conn.ClientAddr = cliAddr
	srvudp, err := net.DialUDP("udp", nil, srvAddr)
	if err != nil {
		return nil
	}
	conn.ServerConn = srvudp
	conn.timeout = timeout
	return conn
}

// Start listener, return inbound lisener and proxy target UDP address
func initUDPConnections(listenAddr string, targetAddress string) (*net.UDPConn, *net.UDPAddr, error) {
	// Set up Proxy
	saddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, nil, err
	}
	inboundConn, err := net.ListenUDP("udp", saddr)
	if err != nil {
		return nil, nil, err
	}

	log.Println("[UDP] Proxy listening on " + listenAddr)

	// Custom fork: upstreams are resolved per session (pool_custom.go), so a
	// name that does not resolve right now does not stop the listener
	outboundConn, _ := net.ResolveUDPAddr("udp", targetAddress)

	return inboundConn, outboundConn, nil
}

// Go routine which manages connection from server to single client
func (c *ProxyRelayInstance) RunUDPConnectionRelay(conn *udpClientServerConn, lisenter *net.UDPConn) {
	// Fix for #1207:
	// Release the per-client socket and remove the map entry on start
	saddr := conn.ClientAddr.String()
	defer func() {
		conn.ServerConn.Close()
		if conn.release != nil {
			conn.release()
		}
		// Only delete if the map still points to *this* connection so we never
		// clobber a freshly recreated session for the same client address.
		c.udpClientMap.CompareAndDelete(saddr, conn)
	}()

	var buffer [1500]byte
	for {
		// Refresh the idle deadline before each read
		if conn.timeout > 0 {
			conn.ServerConn.SetReadDeadline(time.Now().Add(conn.timeout))
		}

		// Read from server
		n, err := conn.ServerConn.Read(buffer[0:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				// Socket closed by proxy shutdown or session teardown
				return
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				// Session was idle for longer than the timeout window
				c.LogMsg("[UDP] Idle session for client "+saddr+" expired after "+conn.timeout.String(), nil)
				return
			}

			if errors.Is(err, syscall.ECONNREFUSED) && conn.upstream != "" {
				// Custom fork: ICMP port unreachable from the upstream counts for passive monitoring
				c.reportConnectFailure(conn.upstream, err)
			}
			c.LogMsg("[UDP] Read error for client "+saddr+", closing session: "+err.Error(), nil)
			return
		}
		conn.captureFromUpstream(buffer[0:n]) //Custom fork: traffic capture
		// Relay it to client
		if c.bToaAccumulatedByteTransfer != nil {
			atomic.AddInt64(c.bToaAccumulatedByteTransfer, int64(n)) //Custom fork: count replies too
		}
		_, err = lisenter.WriteToUDP(buffer[0:n], conn.ClientAddr)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				// Inbound listener gone (proxy stopping)
				return
			}
			continue
		}

	}
}

// Close all connections that waiting for read from server
func (c *ProxyRelayInstance) CloseAllUDPConnections() {
	c.udpClientMap.Range(func(clientAddr, clientServerConn interface{}) bool {
		conn := clientServerConn.(*udpClientServerConn)
		conn.ServerConn.Close()
		return true
	})
}

// Write Proxy Protocol v2 header to UDP connection
func WriteProxyProtocolHeaderUDP(conn *net.UDPConn, srcAddr, dstAddr *net.UDPAddr) error {
	header := proxyproto.Header{
		Version:           byte(convertProxyProtocolVersionToInt(ProxyProtocolV2)),
		Command:           proxyproto.PROXY,
		TransportProtocol: proxyproto.UDPv4,
		SourceAddr:        srcAddr,
		DestinationAddr:   dstAddr,
	}
	var buf bytes.Buffer
	_, err := header.WriteTo(&buf)
	if err != nil {
		return err
	}
	_, err = conn.Write(buf.Bytes())
	return err
}

func (c *ProxyRelayInstance) ForwardUDP(address1, address2 string, stopChan chan bool) error {
	//By default the incoming listen Address is int
	//We need to add the loopback address into it
	if isValidPort(address1) {
		//Port number only. Missing the : in front
		address1 = ":" + address1
	}
	if strings.HasPrefix(address1, ":") {
		//Prepend 0.0.0.0 to the address
		address1 = "0.0.0.0" + address1
	}

	lisener, _, err := initUDPConnections(address1, address2)
	if err != nil {
		return err
	}

	go func() {
		//Custom fork: wait for the stop signal (or the channel closing)
		//instead of polling every 100 ms
		<-stopChan
		//Stop server -> client forwarder
		c.CloseAllUDPConnections()
		//Stop client -> server forwarder
		//Force close, will terminate ReadFromUDP for inbound listener
		lisener.Close()
	}()

	var buffer [1500]byte
	for {
		n, cliaddr, err := lisener.ReadFromUDP(buffer[0:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				//Proxy stopped
				return nil
			}
			continue
		}
		atomic.AddInt64(c.aTobAccumulatedByteTransfer, int64(n))
		saddr := cliaddr.String()
		rawConn, found := c.udpClientMap.Load(saddr)
		var conn *udpClientServerConn
		if !found {
			// Check if this new client is allowed by access control policy
			accessRule := c.getEffectiveAccessRule()
			if accessRule != nil && !accessRule.AllowIpAccess(cliaddr.IP.String()) {
				c.LogMsg("[UDP] Connection from "+cliaddr.IP.String()+" rejected by access control policy", nil)
				continue
			}

			//Custom fork: pick an upstream from the pool (pool_custom.go)
			conn = c.newUDPSession(cliaddr)
			if conn == nil {
				continue
			}
			conn.listener, _ = lisener.LocalAddr().(*net.UDPAddr)
			c.setupUDPCapture(conn) //Custom fork: traffic capture (capture_custom.go)
			c.udpClientMap.Store(saddr, conn)
			c.LogMsg("[UDP] Created new connection for client "+saddr, nil)
			// Fire up routine to manage new connection
			go c.RunUDPConnectionRelay(conn, lisener)

			// Send Proxy Protocol header if enabled
			if c.ProxyProtocolVersion == ProxyProtocolV2 {
				if upAddr, ok := conn.ServerConn.RemoteAddr().(*net.UDPAddr); ok {
					_ = WriteProxyProtocolHeaderUDP(conn.ServerConn, cliaddr, upAddr)
				}
			}
		} else {
			//Custom fork: no log line per packet
			conn = rawConn.(*udpClientServerConn)
		}

		// The client just sent a packet, update idle deadline
		if conn.timeout > 0 {
			conn.ServerConn.SetReadDeadline(time.Now().Add(conn.timeout))
		}

		// Relay to server
		conn.captureFromClient(buffer[0:n]) //Custom fork: traffic capture
		_, err = conn.ServerConn.Write(buffer[0:n])
		if err != nil {
			continue
		}

	}
}
