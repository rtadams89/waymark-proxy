package l4proxy

/*
	Instances.go

	This file contains the methods to start, stop, and manage the proxy relay instances.

	Custom fork: the run state (Running and the stop channels) and the rule
	settings are guarded by poolMu, and lifeMu serialises Start and Stop.
	Stop closes the stop channels (a close never blocks, even when a
	listener already failed) and waits for the listeners to exit, so a
	restart can bind the same address straight away.
*/

import (
	"errors"
	"log"
	"sync"
	"time"
)

// listenerExitTimeout bounds how long Stop waits for the listeners to exit.
const listenerExitTimeout = 5 * time.Second

func (c *ProxyRelayInstance) LogMsg(message string, originalError error) {
	c.poolMu.RLock()
	enabled := c.EnableLogging
	c.poolMu.RUnlock()
	if !enabled {
		return
	}

	if originalError != nil {
		log.Println(message, "error:", originalError)
	} else {
		log.Println(message)
	}
}

// Start a proxy if stopped
func (c *ProxyRelayInstance) Start() error {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()

	c.poolMu.Lock()
	if c.tcpStopChan != nil || c.udpStopChan != nil {
		c.Running = true
		c.poolMu.Unlock()
		return errors.New("proxy already running")
	}

	if c.aTobAccumulatedByteTransfer == nil {
		aAcc := int64(0)
		c.aTobAccumulatedByteTransfer = &aAcc
	}
	if c.bToaAccumulatedByteTransfer == nil {
		bAcc := int64(0)
		c.bToaAccumulatedByteTransfer = &bAcc
	}

	listenAddr, targetAddr := c.ListeningAddress, c.ProxyTargetAddr
	listeners := &sync.WaitGroup{}
	c.listeners = listeners

	//Start the proxy service
	if c.UseUDP {
		udpStopChan := make(chan bool)
		c.udpStopChan = udpStopChan
		listeners.Add(1)
		go func() {
			defer listeners.Done()
			if err := c.ForwardUDP(listenAddr, targetAddr, udpStopChan); err != nil {
				c.listenerFailed(udpStopChan, "udp", err)
			}
		}()
	}

	if c.UseTCP {
		tcpStopChan := make(chan bool)
		c.tcpStopChan = tcpStopChan
		listeners.Add(1)
		go func() {
			defer listeners.Done()
			//Default to transport mode
			if err := c.Port2host(listenAddr, targetAddr, tcpStopChan); err != nil {
				c.listenerFailed(tcpStopChan, "tcp", err)
			}
		}()
	}

	//Successfully spawned off the proxy routine
	c.Running = c.tcpStopChan != nil || c.udpStopChan != nil
	c.poolMu.Unlock()

	c.parent.SaveConfigToDatabase()
	c.parent.changed() //Custom fork: health checks follow running rules
	return nil
}

// listenerFailed records that the listener owning stopChan could not start.
// The rule keeps running while its other protocol still listens.
func (c *ProxyRelayInstance) listenerFailed(stopChan chan bool, proto string, err error) {
	c.poolMu.Lock()
	switch {
	case proto == "udp" && c.udpStopChan == stopChan:
		c.udpStopChan = nil
	case proto == "tcp" && c.tcpStopChan == stopChan:
		c.tcpStopChan = nil
	default:
		//Stopped or restarted meanwhile
		c.poolMu.Unlock()
		return
	}
	c.Running = c.tcpStopChan != nil || c.udpStopChan != nil
	name, id := c.Name, c.UUID
	c.poolMu.Unlock()
	c.parent.SaveConfigToDatabase()
	c.parent.changed()
	c.parent.logf("[proto:"+proto+"] Error starting Layer 4 rule "+name+"("+id+")", err)
}

// Return if a proxy config is running
func (c *ProxyRelayInstance) IsRunning() bool {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	return c.tcpStopChan != nil || c.udpStopChan != nil
}

// Restart a proxy config
func (c *ProxyRelayInstance) Restart() {
	if c.IsRunning() {
		c.Stop()
	}
	c.Start()
}

// Stop a running proxy if running
func (c *ProxyRelayInstance) Stop() {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()

	c.poolMu.Lock()
	name := c.Name
	udpStop, tcpStop, listeners := c.udpStopChan, c.tcpStopChan, c.listeners
	c.udpStopChan, c.tcpStopChan, c.listeners = nil, nil, nil
	c.Running = false
	c.poolMu.Unlock()

	c.parent.logf("Stopping Layer 4 rule "+name, nil)
	if udpStop != nil {
		close(udpStop)
	}
	if tcpStop != nil {
		close(tcpStop)
	}
	if listeners != nil {
		done := make(chan struct{})
		go func() {
			listeners.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(listenerExitTimeout):
			c.parent.logf("Layer 4 rule "+name+": listeners did not exit in time", nil)
		}
	}
	c.parent.logf("Stopped Layer 4 rule "+name, nil)

	//Update the running status
	c.parent.SaveConfigToDatabase()
	c.parent.changed() //Custom fork
}
