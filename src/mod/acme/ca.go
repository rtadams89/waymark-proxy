package acme

// CA definitions loaded from the embedded ca.json.
import (
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"strings"
)

type CaDef struct {
	Production map[string]string
	Test       map[string]string
}

//go:embed ca.json
var caJson []byte

var caDef CaDef = CaDef{}

func init() {
	runtimeCaDef := CaDef{}
	err := json.Unmarshal(caJson, &runtimeCaDef)
	if err != nil {
		log.Println("[ERR] Unable to unmarshal CA def from embedded file. You sure your ca.json is valid?")
		return
	}

	caDef = runtimeCaDef
}

// loadCAApiServerFromName returns a CA's ACME directory URL.
func loadCAApiServerFromName(caName string, acmeTestMode bool) (string, error) {
	// Buypass certificates name the organization "Buypass AS-983163327"
	if strings.HasPrefix(caName, "Buypass AS") {
		caName = "Buypass"
	}

	val, ok := caDef.Production[caName]
	if acmeTestMode {
		val, ok = caDef.Test[caName]
	}

	if !ok {
		return "", errors.New("This CA is not supported")
	}

	return val, nil
}

func IsSupportedCA(caName string, acmeTestMode bool) bool {
	_, err := loadCAApiServerFromName(caName, acmeTestMode)
	return err == nil
}
