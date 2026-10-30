package acme

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

type AutoRenewConfig struct {
	Enabled      bool
	Email        string
	RenewAll     bool // renew all, or only FilesToRenew
	FilesToRenew []string
	DNSServers   string
}

type AutoRenewer struct {
	ConfigFilePath    string
	CertFolder        string
	AcmeHandler       *ACMEHandler
	RenewerConfig     *AutoRenewConfig
	RenewTickInterval int64
	EarlyRenewDays    int // days before expiry to renew
	TickerstopChan    chan bool
	Logger            *logger.Logger
}

type ExpiredCerts struct {
	Domains  []string
	Filepath string
}

// NewAutoRenewer creates an auto renewer; renewCheckInterval is in seconds
// (0 for one day).
func NewAutoRenewer(config string, certFolder string, renewCheckInterval int64, earlyRenewDays int, AcmeHandler *ACMEHandler, logger *logger.Logger) (*AutoRenewer, error) {
	if renewCheckInterval == 0 {
		renewCheckInterval = 86400
	}

	if earlyRenewDays == 0 {
		earlyRenewDays = 30
	}

	if !utils.FileExists(config) {
		os.MkdirAll(filepath.Dir(config), 0775)
		newConfig := AutoRenewConfig{
			RenewAll:     true,
			FilesToRenew: []string{},
		}
		js, _ := json.MarshalIndent(newConfig, "", " ")
		err := utils.WriteFileAtomic(config, js, 0644)
		if err != nil {
			return nil, errors.New("Failed to create acme auto renewer config: " + err.Error())
		}
	}

	renewerConfig := AutoRenewConfig{}
	content, err := os.ReadFile(config)
	if err != nil {
		return nil, errors.New("Failed to open acme auto renewer config: " + err.Error())
	}

	err = json.Unmarshal(content, &renewerConfig)
	if err != nil {
		return nil, errors.New("Malformed acme config file: " + err.Error())
	}

	thisRenewer := AutoRenewer{
		ConfigFilePath:    config,
		CertFolder:        certFolder,
		AcmeHandler:       AcmeHandler,
		RenewerConfig:     &renewerConfig,
		RenewTickInterval: renewCheckInterval,
		EarlyRenewDays:    earlyRenewDays,
		Logger:            logger,
	}

	thisRenewer.Logf("ACME early renew set to "+fmt.Sprint(earlyRenewDays)+" days and check interval set to "+fmt.Sprint(renewCheckInterval)+" seconds", nil)

	if thisRenewer.RenewerConfig.Enabled {
		thisRenewer.StartAutoRenewTicker()

		go func() {
			if !thisRenewer.renewalHeld() {
				thisRenewer.CheckAndRenewCertificates()
			}
		}()
	}

	return &thisRenewer, nil
}

func (a *AutoRenewer) Logf(message string, err error) {
	a.Logger.PrintAndLog("acme", message, err)
}

func (a *AutoRenewer) StartAutoRenewTicker() {
	if a.TickerstopChan != nil {
		a.TickerstopChan <- true
	}

	time.Sleep(1 * time.Second)

	ticker := time.NewTicker(time.Duration(a.RenewTickInterval) * time.Second)
	done := make(chan bool)

	go func(a *AutoRenewer) {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if a.renewalHeld() {
					continue
				}
				a.Logf("Check and renew certificates in progress", nil)
				a.CheckAndRenewCertificates()
			}
		}
	}(a)

	a.TickerstopChan = done
}

func (a *AutoRenewer) StopAutoRenewTicker() {
	if a.TickerstopChan != nil {
		a.TickerstopChan <- true
	}

	a.TickerstopChan = nil
}

// HandleSetAutoRenewDomains sets the certificates to renew: opr=setSelected
// with a list of file names, or opr=setAuto for all.
func (a *AutoRenewer) HandleSetAutoRenewDomains(w http.ResponseWriter, r *http.Request) {
	opr, err := utils.PostPara(r, "opr")
	if err != nil {
		utils.SendErrorResponse(w, "Operation not set")
		return
	}

	if opr == "setSelected" {
		files, err := utils.PostPara(r, "domains")
		if err != nil {
			utils.SendErrorResponse(w, "Domains is not defined")
			return
		}

		matchingRuleFiles := []string{}
		err = json.Unmarshal([]byte(files), &matchingRuleFiles)
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}

		a.RenewerConfig.RenewAll = false
		a.RenewerConfig.FilesToRenew = matchingRuleFiles
		a.saveRenewConfigToFile()
		utils.SendOK(w)
	} else if opr == "setAuto" {
		a.RenewerConfig.RenewAll = true
		a.saveRenewConfigToFile()
		utils.SendOK(w)
	} else {
		utils.SendErrorResponse(w, "invalid operation given")
	}

}

// HandleLoadAutoRenewDomains returns the certificates to renew, or ["*"] for all.
func (a *AutoRenewer) HandleLoadAutoRenewDomains(w http.ResponseWriter, r *http.Request) {
	results := []string{}
	if a.RenewerConfig.RenewAll {
		results = append(results, "*")
	} else {
		results = a.RenewerConfig.FilesToRenew
	}

	js, _ := json.Marshal(results)
	utils.SendJSONResponse(w, string(js))
}

func (a *AutoRenewer) HandleRenewPolicy(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(a.RenewerConfig.RenewAll)
	utils.SendJSONResponse(w, string(js))
}

func (a *AutoRenewer) HandleRenewNow(w http.ResponseWriter, r *http.Request) {
	renewedDomains, err := a.CheckAndRenewCertificates()
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	message := "Domains renewed"
	if len(renewedDomains) == 0 {
		message = ("All certificates are up-to-date!")
	} else {
		message = ("The following domains have been renewed: " + strings.Join(renewedDomains, ","))
	}

	js, _ := json.Marshal(message)
	utils.SendJSONResponse(w, string(js))
}

// HandleAutoRenewEnable get and set the auto renew enable state
func (a *AutoRenewer) HandleAutoRenewEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		js, _ := json.Marshal(a.RenewerConfig.Enabled)
		utils.SendJSONResponse(w, string(js))
	} else if r.Method == http.MethodPost {
		val, err := utils.PostBool(r, "enable")
		if err != nil {
			utils.SendErrorResponse(w, "invalid or empty enable state")
		}
		if val {
			if a.RenewerConfig.Email == "" {
				utils.SendErrorResponse(w, "Email is not set")
				return
			}
			a.RenewerConfig.Enabled = true
			a.saveRenewConfigToFile()
			a.Logf("ACME auto renew enabled", nil)
			a.StartAutoRenewTicker()
		} else {
			a.RenewerConfig.Enabled = false
			a.saveRenewConfigToFile()
			a.Logf("ACME auto renew disabled", nil)
			a.StopAutoRenewTicker()
		}
	} else {
		http.Error(w, "405 - Method not allowed", http.StatusMethodNotAllowed)
	}

}

func (a *AutoRenewer) HandleACMEEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		js, _ := json.Marshal(a.RenewerConfig.Email)
		utils.SendJSONResponse(w, string(js))
	} else if r.Method == http.MethodPost {
		email, err := utils.PostPara(r, "set")
		if err != nil {
			utils.SendErrorResponse(w, "invalid or empty email given")
			return
		}

		_, err = mail.ParseAddress(email)
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}

		a.RenewerConfig.Email = email
		a.saveRenewConfigToFile()

		utils.SendOK(w)
	} else {
		http.Error(w, "405 - Method not allowed", http.StatusMethodNotAllowed)
	}
}

// CheckAndRenewCertificates renews expired certificates in the certificate
// folder and returns the renewed ones.
func (a *AutoRenewer) CheckAndRenewCertificates() ([]string, error) {
	certFolder := a.CertFolder
	files, err := os.ReadDir(certFolder)
	if err != nil {
		a.Logf("Read certificate store failed", err)
		return []string{}, err
	}

	expiredCertList := []*ExpiredCerts{}
	if a.RenewerConfig.RenewAll {
		for _, file := range files {
			if filepath.Ext(file.Name()) == ".crt" || filepath.Ext(file.Name()) == ".pem" {
				certBytes, err := os.ReadFile(filepath.Join(certFolder, file.Name()))
				if err != nil {
					continue
				}
				if CertExpireSoon(certBytes, a.EarlyRenewDays) || CertIsExpired(certBytes) {
					DNSName, err := ExtractDomains(certBytes)
					if err != nil {
						// Skips certificates without DNS names
						a.Logf("Encounted error when trying to resolve DNS name for cert "+file.Name(), err)
						continue
					}

					expiredCertList = append(expiredCertList, &ExpiredCerts{
						Filepath: filepath.Join(certFolder, file.Name()),
						Domains:  DNSName,
					})
				}
			}
		}
	} else {
		for _, file := range files {
			fileName := file.Name()
			certName := fileName[:len(fileName)-len(filepath.Ext(fileName))]
			if contains(a.RenewerConfig.FilesToRenew, certName) {
				certBytes, err := os.ReadFile(filepath.Join(certFolder, file.Name()))
				if err != nil {
					continue
				}
				if CertExpireSoon(certBytes, a.EarlyRenewDays) || CertIsExpired(certBytes) {
					DNSName, err := ExtractDomains(certBytes)
					if err != nil {
						// Skips certificates without DNS names
						a.Logf("Encounted error when trying to resolve DNS name for cert "+file.Name(), err)
						continue
					}

					expiredCertList = append(expiredCertList, &ExpiredCerts{
						Filepath: filepath.Join(certFolder, file.Name()),
						Domains:  DNSName,
					})
				}
			}
		}
	}

	return a.renewExpiredDomains(expiredCertList)
}

func (a *AutoRenewer) Close() {
	if a.TickerstopChan != nil {
		a.TickerstopChan <- true
	}
}

// renewExpiredDomains renews each expired certificate with all its domains.
func (a *AutoRenewer) renewExpiredDomains(certs []*ExpiredCerts) ([]string, error) {
	renewedCertFiles := []string{}
	for _, expiredCert := range certs {
		a.Logf("Renewing "+expiredCert.Filepath+" (Might take a few minutes)", nil)
		fileName := filepath.Base(expiredCert.Filepath)
		certName := fileName[:len(fileName)-len(filepath.Ext(fileName))]

		certInfoFilename := fmt.Sprintf("%s/%s.json", filepath.Dir(expiredCert.Filepath), certName)
		certInfo, err := LoadCertInfoJSON(certInfoFilename)
		if err != nil {
			a.Logf("Renew "+certName+"certificate error, can't get the ACME detail for certificate, trying org section as ca", err)

			if CAName, extractErr := ExtractIssuerNameFromPEM(expiredCert.Filepath); extractErr != nil {
				a.Logf("Extract issuer name for cert error, using default ca", err)
				certInfo = &CertificateInfoJSON{}
			} else {
				certInfo = &CertificateInfoJSON{AcmeName: CAName}
			}
		}

		// Default propagation timeout when missing
		if certInfo.PropTimeout == 0 {
			certInfo.PropTimeout = 300
		}

		var dnsServers string
		if len(certInfo.DNSServers) > 0 {
			dnsServers = strings.Join(certInfo.DNSServers, ",")
		}

		// Adds the domains of the existing certificate
		sanDomains, errSan := ExtractDomainsFromPEM(expiredCert.Filepath)
		if errSan == nil && len(sanDomains) > 0 {
			expiredCert.Domains = sanDomains
			a.Logf("Using SANs from PEM for renewal: "+strings.Join(sanDomains, ","), nil)
		} else {
			a.Logf("Could not extract SANs from PEM for "+fileName+", using original domains", errSan)
		}

		_, err = a.AcmeHandler.ObtainCert(expiredCert.Domains, certName, a.RenewerConfig.Email, certInfo.AcmeName, certInfo.AcmeUrl, certInfo.SkipTLS, certInfo.UseDNS, certInfo.PropTimeout, dnsServers, certInfo.DisableRecursiveNssCheck, certInfo.DisableAuthoritativeNssCheck)
		if err != nil {
			a.Logf("Renew "+fileName+"("+strings.Join(expiredCert.Domains, ",")+") failed", err)
		} else {
			a.Logf("Successfully renewed "+filepath.Base(expiredCert.Filepath), nil)
			renewedCertFiles = append(renewedCertFiles, filepath.Base(expiredCert.Filepath))
		}
	}

	return renewedCertFiles, nil
}

func (a *AutoRenewer) saveRenewConfigToFile() error {
	js, _ := json.MarshalIndent(a.RenewerConfig, "", " ")
	return utils.WriteFileAtomic(a.ConfigFilePath, js, 0644)
}

// HanldeSetEAB sets the external account binding (kid, hmacEncoded) of an ACME directory.
func (a *AutoRenewer) HanldeSetEAB(w http.ResponseWriter, r *http.Request) {
	kid, err := utils.GetPara(r, "kid")
	if err != nil {
		utils.SendErrorResponse(w, "kid not set")
		return
	}

	hmacEncoded, err := utils.GetPara(r, "hmacEncoded")
	if err != nil {
		utils.SendErrorResponse(w, "hmacEncoded not set")
		return
	}

	acmeDirectoryURL, err := utils.GetPara(r, "acmeDirectoryURL")
	if err != nil {
		utils.SendErrorResponse(w, "acmeDirectoryURL not set")
		return
	}

	if !a.AcmeHandler.Database.TableExists("acme") {
		a.AcmeHandler.Database.NewTable("acme")
	}

	a.AcmeHandler.Database.Write("acme", acmeDirectoryURL+"_kid", kid)
	a.AcmeHandler.Database.Write("acme", acmeDirectoryURL+"_hmacEncoded", hmacEncoded)

	utils.SendOK(w)

}
