// Package acme handles the acme issuing process
package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/challenge/dns01"
	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/registration"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/utils"
)

var defaultNameservers = []string{
	"8.8.8.8:53", // Google DNS
	"8.8.4.4:53", // Google DNS
	"1.1.1.1:53", // Cloudflare DNS
	"1.0.0.1:53", // Cloudflare DNS
}

type CertificateInfoJSON struct {
	AcmeName                     string   `json:"acme_name"`                       // ACME provider name
	AcmeUrl                      string   `json:"acme_url"`                        // Custom ACME URL (if any)
	SkipTLS                      bool     `json:"skip_tls"`                        // Skip TLS verification of upstream
	UseDNS                       bool     `json:"dns"`                             // Use DNS challenge
	PropTimeout                  int      `json:"prop_time"`                       // Propagation timeout
	DNSServers                   []string `json:"dnsServers"`                      // DNS servers
	DisableRecursiveNssCheck     bool     `json:"disable_recursive_nss_check"`     // Disable Recursive NSs Propagation Check
	DisableAuthoritativeNssCheck bool     `json:"disable_authoritative_nss_check"` // Disable Authoritative NSs Propagation Check
}

// ACMEHandler handles ACME-related operations.
type ACMEHandler struct {
	Port           string
	Database       *database.Database
	Logger         *logger.Logger
	TestMode       bool
	KeyFileMode    os.FileMode
	PublicFileMode os.FileMode
}

// NewACME creates a new ACMEHandler instance.
func NewACME(
	port string, database *database.Database,
	logger *logger.Logger, testMode bool, keyFileMode os.FileMode, publicFileMode os.FileMode,
) *ACMEHandler {
	return &ACMEHandler{
		Port:           port,
		Database:       database,
		Logger:         logger,
		TestMode:       testMode,
		KeyFileMode:    keyFileMode,
		PublicFileMode: publicFileMode,
	}
}

func (a *ACMEHandler) Logf(message string, err error) {
	a.Logger.PrintAndLog("acme", message, err)
}

func (a *ACMEHandler) writeFileWithMode(filename string, data []byte, mode os.FileMode) error {
	return utils.WriteFileAtomic(filename, data, mode)
}

// ObtainCert obtains a certificate for the specified domains.
func (a *ACMEHandler) ObtainCert(domains []string, certificateName string, email string, caName string, caUrl string, skipTLS bool, useDNS bool, propagationTimeout int, dnsServers string, disableRecursiveNssCheck bool, disableAuthoritativeNssCheck bool) (bool, error) {
	ctx := context.TODO()

	a.Logf("Obtaining certificate for: "+strings.Join(domains, ", "), nil)

	// Defaults to Let's Encrypt
	if caName == "" {
		caName = "Let's Encrypt"
	}

	if caName == "custom" {
		a.Logf("Using Custom ACME "+caUrl+" for CA Directory URL", nil)
	} else if caUrl == "" {
		// Loads the CA URL from ca.json
		caLinkOverwrite, err := loadCAApiServerFromName(caName, a.TestMode)
		if err == nil {
			caUrl = caLinkOverwrite
			a.Logf("Using "+caLinkOverwrite+" for CA Directory URL", nil)
		} else {
			caUrl, _ = loadCAApiServerFromName("Let's Encrypt", a.TestMode)
			a.Logf("Using Default ACME "+caUrl+" for CA Directory URL", nil)
		}
	} else {
		a.Logf("Using "+caUrl+" for CA Directory URL", nil)
	}

	// Reuses a stored account for this CA, or generates a new key
	var adminUser ACMEUser
	accountLoaded := false
	if loadedKey, loadedReg, ok := a.loadACMEAccount(caUrl, email); ok {
		adminUser = ACMEUser{
			Email:        email,
			key:          loadedKey,
			Registration: loadedReg,
		}
		accountLoaded = true
		a.Logf("Reusing existing ACME account for "+caUrl, nil)
	} else {
		privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			a.Logf("Private key generation failed", err)
			return false, err
		}
		adminUser = ACMEUser{
			Email: email,
			key:   privateKey,
		}
	}

	config := lego.NewConfig(&adminUser)
	config.CADirURL = caUrl

	if skipTLS {
		a.Logf("Ignoring TLS/SSL Verification Error for ACME Server", nil)
		config.HTTPClient.Transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	}

	client, err := lego.NewClient(config)
	if err != nil {
		a.Logf("Failed to spawn new ACME client from current config", err)
		return false, err
	}

	certInfo, err := LoadCertInfoJSON(fmt.Sprintf("./conf/certs/%s.json", certificateName))
	if err == nil {
		useDNS = certInfo.UseDNS
		if dnsServers == "" && certInfo.DNSServers != nil && len(certInfo.DNSServers) > 0 {
			dnsServers = strings.Join(certInfo.DNSServers, ",")
		}
		if certInfo.PropTimeout > 0 {
			propagationTimeout = certInfo.PropTimeout
		}
		if certInfo.DisableRecursiveNssCheck {
			disableRecursiveNssCheck = certInfo.DisableRecursiveNssCheck
		}
		if certInfo.DisableAuthoritativeNssCheck {
			disableAuthoritativeNssCheck = certInfo.DisableAuthoritativeNssCheck
		}
	}

	// Keeps at least one NS propagation check enabled
	if disableAuthoritativeNssCheck && disableRecursiveNssCheck {
		a.Logf("DisableAuthoritativeNssCheck requires recursive NSs check to be enabled; forcing DisableRecursiveNssCheck = false", nil)
		disableRecursiveNssCheck = false
	}

	dnsNameservers := strings.Split(dnsServers, ",")
	for i := range dnsNameservers {
		dnsNameservers[i] = strings.TrimSpace(dnsNameservers[i])
	}

	if useDNS {
		// From the credential set bound to the certificate
		dnsProvider, dnsCredentials, credSource, err := a.resolveDNSCredentials(certificateName)
		if err != nil {
			a.Logf("Resolve DNS credentials failed", err)
			return false, err
		}
		a.Logf("Using "+credSource+" for the DNS challenge", nil)

		provider, err := GetDnsChallengeProviderByName(dnsProvider, dnsCredentials, propagationTimeout)
		if err != nil {
			a.Logf("Unable to resolve DNS challenge provider", err)
			return false, err
		}

		opts := &dns01.Options{}

		if len(dnsNameservers) > 0 && dnsNameservers[0] != "" {
			a.Logf("Using DNS servers: "+strings.Join(dnsNameservers, ", "), nil)
			opts.RecursiveNameservers = dnsNameservers
		} else {
			opts.RecursiveNameservers = defaultNameservers
		}

		dns01.SetDefaultClient(dns01.NewClient(opts))

		dnsOpts := []dns01.ChallengeOption{}
		if disableRecursiveNssCheck {
			dnsOpts = append(dnsOpts, dns01.DisableRecursiveNSsPropagationRequirement())
		}
		if disableAuthoritativeNssCheck {
			dnsOpts = append(dnsOpts, dns01.DisableAuthoritativeNssPropagationRequirement())
		}
		err = client.Challenge.SetDNS01Provider(provider, dnsOpts...)
		if err != nil {
			a.Logf("Failed to resolve DNS01 Provider", err)
			return false, err
		}
	} else {
		err = client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", a.Port))
		if err != nil {
			a.Logf("Failed to resolve HTTP01 Provider", err)
			return false, err
		}
	}

	var reg *acme.ExtendedAccount
	// Registers a new account unless one was loaded
	if accountLoaded {
		reg = adminUser.Registration
	} else if client.GetServerMetadata().ExternalAccountRequired {
		a.Logf("External Account Required for this ACME Provider", nil)
		// External account binding (KID and HMAC)

		if !a.Database.TableExists("acme") {
			a.Database.NewTable("acme")
			return false, errors.New("kid and HmacEncoded configuration required for ACME Provider (Error -1)")
		}

		if !a.Database.KeyExists("acme", config.CADirURL+"_kid") || !a.Database.KeyExists("acme", config.CADirURL+"_hmacEncoded") {
			return false, errors.New("kid and HmacEncoded configuration required for ACME Provider (Error -2)")
		}

		var kid string
		var hmacEncoded string
		err := a.Database.Read("acme", config.CADirURL+"_kid", &kid)
		if err != nil {
			a.Logf("Failed to read kid from database", err)
			return false, err
		}

		err = a.Database.Read("acme", config.CADirURL+"_hmacEncoded", &hmacEncoded)
		if err != nil {
			a.Logf("Failed to read HMAC from database", err)
			return false, err
		}

		a.Logf("EAB Credential retrieved: "+kid+" / "+hmacEncoded, nil)
		if kid != "" && hmacEncoded != "" {
			reg, err = client.Registration.RegisterWithExternalAccountBinding(ctx, registration.RegisterEABOptions{
				TermsOfServiceAgreed: true,
				Kid:                  kid,
				HmacEncoded:          hmacEncoded,
			})
		}
		if err != nil {
			a.Logf("Register with external account binder failed", err)
			return false, err
		}
	} else {
		reg, err = client.Registration.Register(ctx, registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			a.Logf("Unable to register client", err)
			return false, err
		}
	}
	adminUser.Registration = reg

	// Stores a newly registered account
	if !accountLoaded && reg != nil {
		if serr := a.saveACMEAccount(config.CADirURL, &adminUser); serr != nil {
			a.Logf("Failed to persist ACME account (may hit account create limits, try a different IP)", serr)
		} else {
			a.Logf("Persisted ACME account for reuse on "+config.CADirURL, nil)
		}
	}

	request := certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
		KeyType: certcrypto.RSA2048,
	}

	certificates, err := client.Certificate.Obtain(ctx, request)
	if err != nil {
		a.Logf("Obtain certificate failed", err)
		return false, err
	}

	err = a.writeFileWithMode("./conf/certs/"+certificateName+".pem", certificates.Certificate, a.PublicFileMode)
	if err != nil {
		a.Logf("Failed to write public key to disk", err)
		return false, err
	}
	err = a.writeFileWithMode("./conf/certs/"+certificateName+".key", certificates.PrivateKey, a.KeyFileMode)
	if err != nil {
		a.Logf("Failed to write private key to disk", err)
		return false, err
	}

	certInfo = &CertificateInfoJSON{
		AcmeName:                     caName,
		AcmeUrl:                      caUrl,
		SkipTLS:                      skipTLS,
		UseDNS:                       useDNS,
		PropTimeout:                  propagationTimeout,
		DNSServers:                   dnsNameservers,
		DisableRecursiveNssCheck:     disableRecursiveNssCheck,
		DisableAuthoritativeNssCheck: disableAuthoritativeNssCheck,
	}

	certInfoBytes, err := json.Marshal(certInfo)
	if err != nil {
		a.Logf("Marshal certificate renew config failed", err)
		return false, err
	}

	err = a.writeFileWithMode("./conf/certs/"+certificateName+".json", certInfoBytes, a.PublicFileMode)
	if err != nil {
		a.Logf("Failed to write certificate renew config to file", err)
		return false, err
	}

	return true, nil
}

// CheckCertificate returns the domains of expired certificates.
func (a *ACMEHandler) CheckCertificate() []string {
	filenames, err := os.ReadDir("./conf/certs/")

	expiredCerts := []string{}

	if err != nil {
		a.Logf("Failed to load certificate folder", err)
		return []string{}
	}

	for _, filename := range filenames {
		certFilepath := filepath.Join("./conf/certs/", filename.Name())

		certBytes, err := os.ReadFile(certFilepath)
		if err != nil {
			continue
		} else {
			block, _ := pem.Decode(certBytes)
			if block != nil {
				cert, err := x509.ParseCertificate(block.Bytes)
				if err == nil {
					elapsed := time.Since(cert.NotAfter)
					if elapsed > 0 {
						for _, dnsName := range cert.DNSNames {
							if !contains(expiredCerts, dnsName) {
								expiredCerts = append(expiredCerts, dnsName)
							}
						}
						if !contains(expiredCerts, cert.Subject.CommonName) {
							expiredCerts = append(expiredCerts, cert.Subject.CommonName)
						}
					}
				}
			}
		}
	}

	return expiredCerts
}

func (a *ACMEHandler) Getport() string {
	return a.Port
}

func contains(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

// HandleGetExpiredDomains returns the domains of expired certificates.
func (a *ACMEHandler) HandleGetExpiredDomains(w http.ResponseWriter, r *http.Request) {
	type ExpiredDomains struct {
		Domain []string `json:"domain"`
	}

	info := ExpiredDomains{
		Domain: a.CheckCertificate(),
	}

	js, _ := json.MarshalIndent(info, "", " ")
	utils.SendJSONResponse(w, string(js))
}

// HandleRenewCertificate obtains a certificate for the posted domains.
func (a *ACMEHandler) HandleRenewCertificate(w http.ResponseWriter, r *http.Request) {
	domainPara, err := utils.PostPara(r, "domains")

	cleanedDomains := []string{}
	if domainPara != "" {
		for _, d := range strings.Split(domainPara, ",") {
			nd, err := netutils.NormalizeDomain(d)
			if err != nil {
				utils.SendErrorResponse(w, err.Error())
				return
			}
			cleanedDomains = append(cleanedDomains, nd)
		}
	}

	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	filename, err := utils.PostPara(r, "filename")
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	filename = strings.ReplaceAll(filename, "*", "_")
	if !isValidCertName(filename) {
		utils.SendErrorResponse(w, "invalid certificate name")
		return
	}

	email, err := utils.PostPara(r, "email")
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	var caUrl string

	ca, err := utils.PostPara(r, "ca")
	if err != nil {
		a.Logf("CA not set. Using default", nil)
		ca, caUrl = "", ""
	}

	if ca == "custom" {
		caUrl, err = utils.PostPara(r, "caURL")
		if err != nil {
			a.Logf("Custom CA set but no URL provide, Using default", nil)
			ca, caUrl = "", ""
		}
	}

	if ca == "" {
		ca = "Let's Encrypt"
	}

	skipTLS, _ := utils.PostBool(r, "skipTLS")
	dns, _ := utils.PostBool(r, "dns")

	propagationTimeout := 600
	if dns {
		ppgTimeout, err := utils.PostPara(r, "ppgTimeout")
		if err == nil {
			propagationTimeout, err = strconv.Atoi(ppgTimeout)
			if err != nil {
				utils.SendErrorResponse(w, "Invalid propagation timeout value")
				return
			}
			if propagationTimeout < 60 {
				propagationTimeout = 60
			}
		}
	}

	// Adds the domains of the existing certificate
	pemPath := fmt.Sprintf("./conf/certs/%s.pem", filename)
	sanDomains, err := ExtractDomainsFromPEM(pemPath)
	if err == nil {
		domainSet := map[string]struct{}{}
		for _, d := range cleanedDomains {
			domainSet[d] = struct{}{}
		}
		for _, d := range sanDomains {
			domainSet[d] = struct{}{}
		}

		cleanedDomains = []string{}
		for d := range domainSet {
			cleanedDomains = append(cleanedDomains, d)
		}

		a.Logf("Renewal domains including SANs from PEM: "+strings.Join(cleanedDomains, ","), nil)
	} else {
		a.Logf("Could not extract SANs from PEM, using domainPara only", err)
	}

	var dnsServers []string
	dnsServersPara, err := utils.PostPara(r, "dnsServers")
	if err == nil && dnsServersPara != "" {
		dnsServers = strings.Split(dnsServersPara, ",")
		for i := range dnsServers {
			dnsServers[i] = strings.TrimSpace(dnsServers[i])
		}
	}

	dnsServersString := strings.Join(dnsServers, ",")

	disableRecursiveNssCheck, _ := utils.PostBool(r, "disable_recursive_nss_check")
	disableAuthoritativeNssCheck, _ := utils.PostBool(r, "disable_authoritative_nss_check")

	result, err := a.ObtainCert(cleanedDomains, filename, email, ca, caUrl, skipTLS, dns, propagationTimeout, dnsServersString, disableRecursiveNssCheck, disableAuthoritativeNssCheck)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendJSONResponse(w, strconv.FormatBool(result))
}

func IsPortInUse(port int) bool {
	address := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return true
	}
	defer listener.Close()
	return false
}

func LoadCertInfoJSON(filename string) (*CertificateInfoJSON, error) {
	certInfoBytes, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	certInfo := &CertificateInfoJSON{}
	if err = json.Unmarshal(certInfoBytes, certInfo); err != nil {
		return nil, err
	}

	for i := range certInfo.DNSServers {
		certInfo.DNSServers[i] = strings.TrimSpace(certInfo.DNSServers[i])
	}

	return certInfo, nil
}
