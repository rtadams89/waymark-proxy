package acme

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/go-acme/lego/v5/challenge"
	"imuslab.com/zoraxy/mod/acme/acmedns"
)

// GetDnsChallengeProviderByName returns the DNS challenge provider configured
// with the JSON credentials.
func GetDnsChallengeProviderByName(dnsProvider string, dnsCredentials string, ppgTimeout int) (challenge.Provider, error) {
	var dnsCredentialsMap map[string]any
	err := json.Unmarshal([]byte(dnsCredentials), &dnsCredentialsMap)
	if err != nil {
		return nil, err
	}

	// Removes PollingInterval and PropagationTimeout from the credentials
	userDefinedPollingInterval := 2
	if dnsCredentialsMap["PollingInterval"] != nil {
		userDefinedPollingIntervalRaw := dnsCredentialsMap["PollingInterval"].(string)
		delete(dnsCredentialsMap, "PollingInterval")
		convertedPollingInterval, err := strconv.Atoi(userDefinedPollingIntervalRaw)
		if err == nil {
			userDefinedPollingInterval = convertedPollingInterval
		}
	}

	userDefinedPropagationTimeout := ppgTimeout
	if dnsCredentialsMap["PropagationTimeout"] != nil {
		userDefinedPropagationTimeoutRaw := dnsCredentialsMap["PropagationTimeout"].(string)
		delete(dnsCredentialsMap, "PropagationTimeout")
		convertedPropagationTimeout, err := strconv.Atoi(userDefinedPropagationTimeoutRaw)
		if err == nil {
			userDefinedPropagationTimeout = convertedPropagationTimeout
		}
	}

	providerConfigStructure, err := acmedns.GetProviderConfigStructure(dnsProvider)
	if err != nil {
		return nil, err
	}

	var hostURL *url.URL = nil
	for configTitle, configDataType := range providerConfigStructure {
		if configDataType == "*url.URL" {
			// Moves the first *url.URL field out of the credentials into hostURL
			urlStrRaw := dnsCredentialsMap[configTitle]
			if urlStrRaw == nil {
				continue
			}
			urlStr := urlStrRaw.(string)
			delete(dnsCredentialsMap, configTitle)
			hostURL, err = url.Parse(urlStr)
			if err != nil {
				return nil, err
			}
			break
		}
	}

	dnsCredentialsBytes, err := json.Marshal(dnsCredentialsMap)
	if err != nil {
		return nil, err
	}
	dnsCredentials = string(dnsCredentialsBytes)

	return acmedns.GetDNSProviderByJsonConfig(
		dnsProvider,
		dnsCredentials,
		int64(userDefinedPropagationTimeout),
		int64(userDefinedPollingInterval),
		hostURL,
	)
}
