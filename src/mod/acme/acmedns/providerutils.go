package acmedns

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"

	"imuslab.com/zoraxy/mod/utils"
)

//go:embed providers.json
var providers []byte

type ConfigTemplate struct {
	Name             string `json:"Name"`
	ConfigableFields []struct {
		Title    string `json:"Title"`
		Datatype string `json:"Datatype"`
	} `json:"ConfigableFields"`
	HiddenFields []struct {
		Title    string `json:"Title"`
		Datatype string `json:"Datatype"`
	} `json:"HiddenFields"`
}

// GetProviderConfigStructure returns a provider's config fields and their types.
func GetProviderConfigStructure(providerName string) (map[string]string, error) {
	configTemplateMap := map[string]ConfigTemplate{}
	err := json.Unmarshal(providers, &configTemplateMap)
	if err != nil {
		return map[string]string{}, err
	}

	targetConfigTemplate, ok := configTemplateMap[providerName]
	if !ok {
		return map[string]string{}, errors.New("provider not supported")
	}

	results := map[string]string{}
	for _, field := range targetConfigTemplate.ConfigableFields {
		results[field.Title] = field.Datatype
	}

	return results, nil
}

// HandleServeProvidersJson returns the provider names, or one provider's
// config fields.
func HandleServeProvidersJson(w http.ResponseWriter, r *http.Request) {
	providerName, _ := utils.GetPara(r, "name")
	if providerName == "" {
		configTemplateMap := map[string]ConfigTemplate{}
		err := json.Unmarshal(providers, &configTemplateMap)
		if err != nil {
			utils.SendErrorResponse(w, "failed to load DNS provider")
			return
		}

		providers := []string{}
		for providerName, _ := range configTemplateMap {
			providers = append(providers, providerName)
		}

		js, _ := json.Marshal(providers)
		utils.SendJSONResponse(w, string(js))
		return
	}
	confTemplate, err := GetProviderConfigStructure(providerName)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	js, _ := json.Marshal(confTemplate)
	utils.SendJSONResponse(w, string(js))
}
