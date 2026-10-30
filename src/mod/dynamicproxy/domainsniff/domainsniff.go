package domainsniff

// Package domainsniff detects domain and application specific behavior.
import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/utils"
)

// DomainIsSelfSigned reports whether a domain's TLS certificate fails
// verification but completes an unverified handshake.
func DomainIsSelfSigned(domain string) bool {
	host, port, err := net.SplitHostPort(domain)
	if err != nil {
		host = domain
	} else {
		domain = host + ":" + port
	}
	if !strings.Contains(domain, ":") {
		domain = domain + ":443"
	}

	conn, err := net.Dial("tcp", domain)
	if err != nil {
		return false
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, nil)
	err = tlsConn.Handshake()
	if err == nil {
		fmt.Println()
		return false
	}

	config := &tls.Config{
		InsecureSkipVerify: true,
	}
	tlsConn = tls.Client(conn, config)
	err = tlsConn.Handshake()
	return err == nil
}

// DomainUsesTLS reports whether targetURL answers over HTTPS.
func DomainUsesTLS(targetURL string) bool {
	httpsUrl := fmt.Sprintf("https://%s", targetURL)
	httpUrl := fmt.Sprintf("http://%s", targetURL)

	client := http.Client{Timeout: 5 * time.Second}

	resp, err := client.Head(httpsUrl)
	if err == nil && resp.StatusCode == http.StatusOK {
		return true
	}

	resp, err = client.Head(httpUrl)
	if err == nil && resp.StatusCode == http.StatusOK {
		return false
	}

	return false
}

// HandleCheckSiteSupportTLS reports whether a site supports TLS, and with
// selfsignchk also whether its certificate is self-signed.
func HandleCheckSiteSupportTLS(w http.ResponseWriter, r *http.Request) {
	targetURL, err := utils.PostPara(r, "url")
	if err != nil {
		utils.SendErrorResponse(w, "invalid url given")
		return
	}

	_, err = utils.PostBool(r, "selfsignchk")
	if err == nil {
		type result struct {
			Protocol string `json:"protocol"`
			SelfSign bool   `json:"selfsign"`
		}

		scanResult := result{Protocol: "http", SelfSign: false}

		if DomainUsesTLS(targetURL) {
			scanResult.Protocol = "https"
			if DomainIsSelfSigned(targetURL) {
				scanResult.SelfSign = true
			}
		}

		js, _ := json.Marshal(scanResult)
		utils.SendJSONResponse(w, string(js))
		return
	}

	if DomainUsesTLS(targetURL) {
		js, _ := json.Marshal("https")
		utils.SendJSONResponse(w, string(js))
		return
	} else {
		js, _ := json.Marshal("http")
		utils.SendJSONResponse(w, string(js))
		return
	}
}
