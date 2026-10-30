package tlscert

// Self-signed certificates for host names and IP addresses, and the
// certificate for a connection without SNI.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/utils"
)

// WriteSelfSignedCertificate writes a self-signed ECDSA P-256 certificate
// for names (host names and IP addresses; the first is the common name) to
// dir/name.pem and its key to dir/name.key.
func WriteSelfSignedCertificate(dir, name string, names []string, validFor time.Duration) error {
	if len(names) == 0 {
		return errors.New("no names for the certificate")
	}
	if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
		return errors.New("invalid certificate name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         names[0],
			Organization:       []string{"Waymark Proxy"},
			OrganizationalUnit: []string{"Self-signed"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0775); err != nil {
		return err
	}
	// Write the key first
	if err := utils.WriteFileAtomic(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), defaultPrivateKeyFileMode); err != nil {
		return err
	}
	return utils.WriteFileAtomic(filepath.Join(dir, name+".pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), defaultPublicCertFileMode)
}

// CertificateNames returns the sorted host names and IP addresses the
// certificate in file covers, or nil if it cannot be read.
func CertificateNames(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}

// serverNameOrLocalIP is the name to look a certificate up by: the SNI
// name, or for a client that sent none (one connecting to an IP address)
// the address it connected to.
func serverNameOrLocalIP(hello interface {
	LocalIP() string
}, sni string) string {
	if sni != "" {
		return sni
	}
	return hello.LocalIP()
}

type helloLocal struct{ conn net.Conn }

func (h helloLocal) LocalIP() string {
	if h.conn == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(h.conn.LocalAddr().String())
	if err != nil {
		return ""
	}
	return host
}
