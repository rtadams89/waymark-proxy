package streamproxy

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

func isValidPort(port string) bool {
	portInt, err := strconv.Atoi(port)
	if err != nil {
		return false
	}

	if portInt < 1 || portInt > 65535 {
		return false
	}

	return true
}

func (c *ProxyRelayInstance) connCopy(conn1 net.Conn, conn2 net.Conn, wg *sync.WaitGroup, accumulator *int64) {
	//Custom fork: always finish (an error used to skip wg.Done, so forward
	//never returned) and close both sides, so the other copy ends too
	defer wg.Done()
	n, _ := io.Copy(conn1, conn2)
	atomic.AddInt64(accumulator, n) //Add to accumulator
	conn1.Close()
	conn2.Close()
	c.LogMsg("[←] close the connect at local:["+conn1.LocalAddr().String()+"] and remote:["+conn1.RemoteAddr().String()+"]", nil)
}

func WriteProxyProtocolHeader(dst net.Conn, src net.Conn, version ProxyProtocolVersion) error {
	clientAddr, ok1 := src.RemoteAddr().(*net.TCPAddr)
	proxyAddr, ok2 := src.LocalAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return errors.New("invalid TCP address for proxy protocol")
	}

	header := proxyproto.Header{
		Version:           byte(convertProxyProtocolVersionToInt(version)),
		Command:           proxyproto.PROXY,
		TransportProtocol: proxyproto.TCPv4,
		SourceAddr:        clientAddr,
		DestinationAddr:   proxyAddr,
	}

	_, err := header.WriteTo(dst)
	return err
}

func (c *ProxyRelayInstance) forward(conn1 net.Conn, conn2 net.Conn, aTob *int64, bToa *int64) {
	msg := fmt.Sprintf("[+] start transmit. [%s],[%s] <-> [%s],[%s]",
		conn1.LocalAddr().String(), conn1.RemoteAddr().String(),
		conn2.LocalAddr().String(), conn2.RemoteAddr().String())
	c.LogMsg(msg, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go c.connCopy(conn1, conn2, &wg, aTob)
	go c.connCopy(conn2, conn1, &wg, bToa)
	wg.Wait()
}

func (c *ProxyRelayInstance) accept(listener net.Listener) (net.Conn, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, err
	}

	// Check if connection is allowed by access control policy
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		accessRule := c.getEffectiveAccessRule()
		if accessRule != nil {
			if !accessRule.AllowConnectionAccess(conn) {
				time.Sleep(300 * time.Millisecond)
				conn.Close()
				c.LogMsg("[x] Connection from "+addr.IP.String()+" rejected by access control policy", nil)
				return nil, errors.New("Connection from " + addr.IP.String() + " rejected by access control policy")
			}
		} else if !c.parent.Options.AccessControlHandler(conn) {
			time.Sleep(300 * time.Millisecond)
			conn.Close()
			c.LogMsg("[x] Connection from "+addr.IP.String()+" rejected by access control policy", nil)
			return nil, errors.New("Connection from " + addr.IP.String() + " rejected by access control policy")
		}
	}

	c.LogMsg("[√] accept a new client. remote address:["+conn.RemoteAddr().String()+"], local address:["+conn.LocalAddr().String()+"]", nil)
	return conn, nil
}

func startListener(address string) (net.Listener, error) {
	log.Println("[+]", "try to start server on:["+address+"]")
	server, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("listen address [" + address + "] faild")
	}
	log.Println("[√]", "start listen at address:["+address+"]")
	return server, nil
}

/*
	Forwarder Functions
*/

/*
portA -> server
server -> portB
*/
func (c *ProxyRelayInstance) Port2host(allowPort string, targetAddress string, stopChan chan bool) error {
	listenerStartingAddr := allowPort
	if isValidPort(allowPort) {
		//number only, e.g. 8080
		listenerStartingAddr = "0.0.0.0:" + allowPort
	} else if strings.HasPrefix(allowPort, ":") && isValidPort(allowPort[1:]) {
		//port number starting with :, e.g. :8080
		listenerStartingAddr = "0.0.0.0" + allowPort
	}

	server, err := startListener(listenerStartingAddr)
	if err != nil {
		return err
	}

	targetAddress = strings.TrimSpace(targetAddress)
	//Custom fork: read the settings once; connections outlive a restart
	c.poolMu.RLock()
	proxyProtocol, timeout := c.ProxyProtocolVersion, c.Timeout
	c.poolMu.RUnlock()

	//Start stop handler
	go func() {
		<-stopChan
		c.LogMsg("[x] Received stop signal. Exiting Port to Host forwarder", nil)
		server.Close()
	}()

	//Start blocking loop for accepting connections
	for {
		conn, err := c.accept(server)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				//Terminate by stop chan. Exit listener loop
				return nil
			}
			//Connection error. Retry
			continue
		}

		go func(targetAddress string) {
			//Custom fork: pick an upstream from the pool (pool_custom.go)
			target, targetAddress, err := c.dialUpstream(conn)
			if err != nil {
				c.LogMsg("[x] no upstream for client ["+conn.RemoteAddr().String()+"]: "+err.Error(), nil)
				conn.Close()
				return
			}
			c.LogMsg("[→] connect target address ["+targetAddress+"] success.", nil)
			release := c.track(targetAddress, target)
			defer release()
			conn, target = c.captureTCP(conn, target, targetAddress) //Custom fork: traffic capture (capture_custom.go)

			if proxyProtocol != ProxyProtocolDisabled {
				c.LogMsg("[+] write proxy protocol header to target address ["+targetAddress+"]", nil)
				err = WriteProxyProtocolHeader(target, conn, proxyProtocol)
				if err != nil {
					c.LogMsg("[x] Write proxy protocol header failed: "+err.Error(), nil)
					target.Close()
					conn.Close()
					c.LogMsg("[←] close the connect at local:["+conn.LocalAddr().String()+"] and remote:["+conn.RemoteAddr().String()+"]", nil)
					time.Sleep(time.Duration(timeout) * time.Second)
					return
				}
			}

			c.forward(target, conn, c.aTobAccumulatedByteTransfer, c.bToaAccumulatedByteTransfer)
		}(targetAddress)
	}
}
