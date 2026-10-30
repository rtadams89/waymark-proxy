package redirection

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

type RuleTable struct {
	AllowRegex    bool     // Allow regular expressions in rule matching
	CaseSensitive bool     //Force case sensitive URL matching
	configPath    string   //The location where the redirection rules is stored
	rules         sync.Map //Store map[string]*RedirectRule for this reverse proxy instance
	Logger        *logger.Logger
}

type RedirectRule struct {
	Enabled           bool   //Whether this redirection rule is enabled
	RedirectURL       string //The matching URL to redirect
	TargetURL         string //The destination redirection url
	ForwardChildpath  bool   //Also redirect the pathname
	StatusCode        int    //Status Code for redirection
	RequireExactMatch bool   //Require exact URL match instead of prefix matching
	DeviceType        string //Device type filter: "all", "desktop", or "mobile"

	parent *RuleTable
}

func NewRuleTable(configPath string, allowRegex bool, caseSensitive bool, logger *logger.Logger) (*RuleTable, error) {
	thisRuleTable := RuleTable{
		rules:         sync.Map{},
		configPath:    configPath,
		AllowRegex:    allowRegex,
		CaseSensitive: caseSensitive,
		Logger:        logger,
	}
	if !utils.FileExists(configPath) {
		os.MkdirAll(configPath, 0775)
	}

	// Load all the *.json from the configPath
	files, err := filepath.Glob(filepath.Join(configPath, "*.json"))
	if err != nil {
		return nil, err
	}

	// Parse the json content into RedirectRule
	var rules []*RedirectRule
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		thisRule := RedirectRule{
			Enabled: true, // Default to enabled when loading from file
		}

		err = json.Unmarshal(b, &thisRule)
		if err != nil {
			continue
		}

		rules = append(rules, &thisRule)
	}

	//Map the rules into the sync map
	for _, rule := range rules {
		rule.parent = &thisRuleTable
		thisRuleTable.log("Redirection rule added: "+rule.RedirectURL+" -> "+rule.TargetURL, nil)
		thisRuleTable.rules.Store(rule.RedirectURL, rule)
	}

	return &thisRuleTable, nil
}

func (t *RuleTable) AddRedirectRule(redirectURL string, destURL string, forwardPathname bool, statusCode int, requireExactMatch bool, deviceType string) error {
	newRule := &RedirectRule{
		Enabled:           true,
		RedirectURL:       redirectURL,
		TargetURL:         destURL,
		ForwardChildpath:  forwardPathname,
		StatusCode:        statusCode,
		RequireExactMatch: requireExactMatch,
		DeviceType:        deviceType,
		parent:            t,
	}

	err := newRule.SaveChangeToFile()
	if err != nil {
		return err
	}

	t.rules.Store(redirectURL, newRule)

	return nil
}

// Edit an existing redirection rule, the oldRedirectURL is used to find the rule to be edited
func (t *RuleTable) EditRedirectRule(oldRedirectURL string, newRedirectURL string, destURL string, forwardPathname bool, statusCode int, requireExactMatch bool, deviceType string) error {
	// Preserve the enabled state from the old rule
	enabled := true
	if oldRule, ok := t.rules.Load(oldRedirectURL); ok {
		enabled = oldRule.(*RedirectRule).Enabled
	}

	newRule := &RedirectRule{
		Enabled:           enabled,
		RedirectURL:       newRedirectURL,
		TargetURL:         destURL,
		ForwardChildpath:  forwardPathname,
		StatusCode:        statusCode,
		RequireExactMatch: requireExactMatch,
		DeviceType:        deviceType,
		parent:            t,
	}

	t.DeleteRedirectRule(oldRedirectURL)

	err := newRule.SaveChangeToFile()
	if err != nil {
		return err
	}

	t.rules.Store(newRedirectURL, newRule)

	return nil
}

func (t *RuleTable) DeleteRedirectRule(redirectURL string) error {
	// Convert the redirectURL to a valid filename by replacing "/" with "-" and "." with "_"
	filename := utils.ReplaceSpecialCharacters(redirectURL) + ".json"

	filepath := path.Join(t.configPath, filename)

	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		return nil // File doesn't exist, nothing to delete
	}

	if err := os.Remove(filepath); err != nil {
		t.log("Error deleting file "+filepath, err)
		return err
	}

	t.rules.Delete(redirectURL)
	return nil
}

// Get a list of all the redirection rules
func (t *RuleTable) GetAllRedirectRules() []*RedirectRule {
	rules := []*RedirectRule{}
	t.rules.Range(func(key, value interface{}) bool {
		r, ok := value.(*RedirectRule)
		if ok {
			rules = append(rules, r)
		}
		return true
	})
	return rules
}

// Toggle the enabled state of a redirection rule, also write to disk
func (t *RuleTable) ToggleEnableRedirectRule(redirectURL string, enabled bool) error {
	ruleInterface, ok := t.rules.Load(redirectURL)
	if !ok {
		return errors.New("redirect rule not found")
	}

	rule := ruleInterface.(*RedirectRule)
	rule.Enabled = enabled

	err := rule.SaveChangeToFile()
	if err != nil {
		return err
	}

	t.rules.Store(redirectURL, rule)
	return nil
}

// Check if a given request URL matched any of the redirection rule
func (t *RuleTable) MatchRedirectRule(requestedURL string) *RedirectRule {
	var targetRedirectionRule *RedirectRule = nil
	var maxMatch int = 0
	t.rules.Range(func(key interface{}, value interface{}) bool {
		rule := value.(*RedirectRule)
		keyStr := key.(string)

		if !rule.Enabled {
			return true
		}

		if t.AllowRegex {
			//Regexp matching rule
			matched, err := regexp.MatchString(keyStr, requestedURL)
			if err != nil {
				t.log("Unable to match regex", err)
				return true
			}
			if matched {
				maxMatch = len(keyStr)
				targetRedirectionRule = rule
			}
			return true
		}

		//Check matching based on exact match requirement
		var matched bool
		if rule.RequireExactMatch {
			if t.CaseSensitive {
				matched = requestedURL == keyStr
			} else {
				matched = strings.EqualFold(requestedURL, keyStr)
			}
			if !matched {
				//Also check for trailing slash case
				if t.CaseSensitive {
					matched = requestedURL == keyStr+"/"
				} else {
					matched = strings.EqualFold(requestedURL, keyStr+"/")
				}
			}
		} else {
			//Default: prefix matching redirect
			if t.CaseSensitive {
				matched = strings.HasPrefix(requestedURL, keyStr)
			} else {
				matched = strings.HasPrefix(strings.ToLower(requestedURL), strings.ToLower(keyStr))
			}
		}

		if matched {
			if len(keyStr) > maxMatch {
				maxMatch = len(keyStr)
				targetRedirectionRule = rule
			}
		}

		return true
	})

	return targetRedirectionRule
}

// Log the message to log file, use STDOUT if logger not set
func (t *RuleTable) log(message string, err error) {
	if t.Logger == nil {
		if err == nil {
			log.Println("[Redirect] " + message)
		} else {
			log.Println("[Redirect] " + message + ": " + err.Error())
		}
	} else {
		t.Logger.PrintAndLog("redirect", message, err)
	}
}

func (r *RedirectRule) SaveChangeToFile() error {
	// Convert the redirectURL to a valid filename by replacing "/" with "-" and "." with "_"
	filename := utils.ReplaceSpecialCharacters(r.RedirectURL) + ".json"
	filepath := path.Join(r.parent.configPath, filename)

	js, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		r.parent.log("Error encoding JSON to file "+filepath, err)
		return err
	}

	err = utils.WriteFileAtomic(filepath, js, 0644)
	if err != nil {
		r.parent.log("Error writing JSON to file "+filepath, err)
		return err
	}

	return nil
}
