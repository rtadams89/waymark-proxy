package acme

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

func ExtractIssuerNameFromPEM(pemFilePath string) (string, error) {
	pemData, err := os.ReadFile(pemFilePath)
	if err != nil {
		return "", err
	}

	return ExtractIssuerName(pemData)
}

// ExtractDomains returns the DNS names of a PEM certificate.
func ExtractDomains(certBytes []byte) ([]string, error) {
	domains := []string{}
	block, _ := pem.Decode(certBytes)
	if block != nil {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return []string{}, err
		}
		for _, dnsName := range cert.DNSNames {
			if !contains(domains, dnsName) {
				domains = append(domains, dnsName)
			}
		}

		return domains, nil
	}
	return []string{}, errors.New("decode cert bytes failed")
}

func ExtractIssuerName(certBytes []byte) (string, error) {
	block, _ := pem.Decode(certBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("failed to decode PEM block containing certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %v", err)
	}

	if len(cert.Issuer.Organization) == 0 {
		return "", fmt.Errorf("cert didn't have org section exist")
	}

	issuer := cert.Issuer.Organization[0]

	return issuer, nil
}

// ExtractDomainsFromPEM reads a PEM certificate file and returns all SANs
func ExtractDomainsFromPEM(pemFilePath string) ([]string, error) {

	certBytes, err := os.ReadFile(pemFilePath)
	if err != nil {
		return nil, err
	}
	domains, err := ExtractDomains(certBytes)
	if err != nil {
		return nil, err
	}
	return domains, nil
}

// CertIsExpired reports whether a PEM certificate is expired.
func CertIsExpired(certBytes []byte) bool {
	block, _ := pem.Decode(certBytes)
	if block != nil {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			elapsed := time.Since(cert.NotAfter)
			if elapsed > 0 {
				return true
			}
		}
	}
	return false
}

// CertExpireSoon reports whether a PEM certificate expires within numberOfDays.
func CertExpireSoon(certBytes []byte, numberOfDays int) bool {
	block, _ := pem.Decode(certBytes)
	if block != nil {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			expirationDate := cert.NotAfter
			threshold := time.Duration(numberOfDays) * 24 * time.Hour

			timeRemaining := time.Until(expirationDate)
			if timeRemaining <= threshold {
				return true
			}
		}
	}
	return false
}
