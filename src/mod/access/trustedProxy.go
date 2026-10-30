package access

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"

	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/utils"
)

//go:embed default_trusted_proxies.csv
var defaultTrustedProxiesCSV string

// IsTrustedProxy reports whether ip matches a trusted proxy IP or CIDR.
func (c *Controller) IsTrustedProxy(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	if _, exists := c.TrustedProxies.Load(ip); exists {
		return true
	}

	c.trustedCIDRMu.RLock()
	defer c.trustedCIDRMu.RUnlock()
	for _, cidr := range c.trustedCIDRs {
		if cidr.Contains(parsedIP) {
			return true
		}
	}

	return false
}

// rebuildTrustedCIDRCache parses the CIDR entries of the trusted proxy list.
func (c *Controller) rebuildTrustedCIDRCache() {
	var cidrs []*net.IPNet
	c.TrustedProxies.Range(func(key, value any) bool {
		proxy, ok := value.(TrustedProxy)
		if !ok {
			return true
		}
		if strings.Contains(proxy.IP, "/") {
			_, cidr, err := net.ParseCIDR(proxy.IP)
			if err == nil {
				cidrs = append(cidrs, cidr)
			}
		}
		return true
	})
	c.trustedCIDRMu.Lock()
	c.trustedCIDRs = cidrs
	c.trustedCIDRMu.Unlock()
}

func (c *Controller) TrustedProxyExists(ip string) bool {
	_, exists := c.TrustedProxies.Load(ip)
	return exists
}

func (c *Controller) AddTrustedProxy(ip string, desc string) bool {
	_, exists := c.TrustedProxies.Load(ip)
	if !exists {
		c.TrustedProxies.Store(ip, TrustedProxy{IP: ip, Desc: desc})
		c.rebuildTrustedCIDRCache()
	}
	return !exists
}

func (c *Controller) RemoveTrustedProxy(ip string) bool {
	_, exists := c.TrustedProxies.Load(ip)
	if exists {
		c.TrustedProxies.Delete(ip)
		c.rebuildTrustedCIDRCache()
	}
	return exists
}

func (c *Controller) ListTrustedProxies() []TrustedProxy {
	var proxies []TrustedProxy
	c.TrustedProxies.Range(func(key, value any) bool {
		proxy, ok := value.(TrustedProxy)
		if ok {
			proxies = append(proxies, proxy)
		}
		return true
	})
	return proxies
}

// LoadTrustedProxies loads the trusted proxies, creating the file from the
// defaults if missing.
func (c *Controller) LoadTrustedProxies() error {
	trustedProxies := []TrustedProxy{}

	if !utils.FileExists(c.Options.TrustedProxiesFile) {
		defaultProxies := loadDefaultTrustedProxiesFromCSV()
		data, err := json.MarshalIndent(defaultProxies, "", "  ")
		if err != nil {
			return err
		}
		err = utils.WriteFileAtomic(c.Options.TrustedProxiesFile, data, 0644)
		if err != nil {
			return err
		}
		for _, proxy := range defaultProxies {
			c.TrustedProxies.Store(proxy.IP, proxy)
		}
		c.rebuildTrustedCIDRCache()
		return nil
	}

	data, err := os.ReadFile(c.Options.TrustedProxiesFile)
	if err != nil {
		return err
	}

	err = json.Unmarshal(data, &trustedProxies)
	if err != nil {
		return err
	}

	for _, proxy := range trustedProxies {
		c.TrustedProxies.Store(proxy.IP, proxy)
	}
	c.rebuildTrustedCIDRCache()

	return nil
}

// GetDefaultTrustedProxies returns the default trusted proxy list.
func (c *Controller) GetDefaultTrustedProxies() []TrustedProxy {
	return loadDefaultTrustedProxiesFromCSV()
}

func loadDefaultTrustedProxiesFromCSV() []TrustedProxy {
	var proxies []TrustedProxy
	scanner := bufio.NewScanner(strings.NewReader(defaultTrustedProxiesCSV))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		proxies = append(proxies, TrustedProxy{
			IP:   line,
			Desc: "Cloudflare",
		})
	}
	return proxies
}

func (c *Controller) SaveTrustedProxies() error {
	trustedProxies := c.ListTrustedProxies()
	data, err := json.MarshalIndent(trustedProxies, "", "  ")
	if err != nil {
		return err
	}

	err = utils.WriteFileAtomic(c.Options.TrustedProxiesFile, data, 0644)
	return err
}

// ClientIP returns the client's address: with trustHeaders, the forwarded
// address when the connection comes from a trusted proxy; otherwise the
// connection's address.
func (c *Controller) ClientIP(r *http.Request, trustHeaders bool) string {
	remoteIP := netutils.GetRequesterIPUntrusted(r)
	if trustHeaders && c != nil && c.IsTrustedProxy(remoteIP) {
		if ip := netutils.GetRequesterIP(r); ip != "" {
			return ip
		}
	}
	return remoteIP
}
