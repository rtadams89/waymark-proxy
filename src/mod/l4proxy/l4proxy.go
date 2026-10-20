package l4proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

/*
	Layer 4 Proxy

	Forward port from one port to another
	Also accept active connection and passive
	connection
*/

// ProxyProtocolVersion enum type
type ProxyProtocolVersion int

const (
	ProxyProtocolDisabled ProxyProtocolVersion = 0
	ProxyProtocolV1       ProxyProtocolVersion = 1
	ProxyProtocolV2       ProxyProtocolVersion = 2
)

type ProxyRelayOptions struct {
	Name                 string
	ListeningAddr        string
	ProxyAddr            string
	Timeout              int
	UseTCP               bool
	UseUDP               bool
	ProxyProtocolVersion ProxyProtocolVersion
	EnableLogging        bool
	AccessRuleUUID       string //The UUID of the access control rule, empty for default rule
}

// ProxyRuleUpdateConfig is used to update the proxy rule config
type ProxyRuleUpdateConfig struct {
	InstanceUUID         string //The target instance UUID to update
	NewName              string //New name for the instance, leave empty for no change
	NewListeningAddr     string //New listening address, leave empty for no change
	NewProxyAddr         string //New proxy target address, leave empty for no change
	UseTCP               bool   //Enable TCP proxy, default to false
	UseUDP               bool   //Enable UDP proxy, default to false
	ProxyProtocolVersion int    //Enable Proxy Protocol v1/v2, default to disabled
	EnableLogging        bool   //Enable Logging TCP/UDP Message, default to true
	NewTimeout           int    //New timeout for the connection, leave -1 for no change
	NewAccessRuleUUID    string //New access rule UUID, leave empty for no change
}

type ProxyRelayInstance struct {
	/* Runtime Config */
	UUID                 string               //A UUIDv4 representing this config
	Name                 string               //Name of the config
	Running              bool                 //Status, read only
	AutoStart            bool                 //If the service suppose to started automatically
	ListeningAddress     string               //Listening Address, usually 127.0.0.1:port
	ProxyTargetAddr      string               //Proxy target address
	UseTCP               bool                 //Enable TCP proxy
	UseUDP               bool                 //Enable UDP proxy
	ProxyProtocolVersion ProxyProtocolVersion //Proxy Protocol v1/v2
	AccessRuleUUID       string               //The UUID of the access control rule, empty for default rule
	EnableLogging        bool                 //Enable logging for ProxyInstance
	Timeout              int                  //Timeout for connection in sec

	/* Custom fork: upstream pool (pool_custom.go) */
	Destinations        []Destination       //Destination servers with weights; Upstreams and ProxyTargetAddr mirror them
	Upstreams           []string            //Every destination address (for older versions)
	LoadBalancing       string              //weighted (default) or leastconn; older modes are converted by normalizePool
	StickySessions      bool                //Keep a client IP address on the same destination
	HealthCheck         *healthcheck.Config //Active checks and passive monitoring; nil for none
	CloseOnUpstreamDown bool                //Close open connections to an upstream when health checks take it out

	/* Internal */
	tcpStopChan                 chan bool //Stop channel for TCP listener
	udpStopChan                 chan bool //Stop channel for UDP listener
	aTobAccumulatedByteTransfer *int64    //Accumulated byte transfer from A to B
	bToaAccumulatedByteTransfer *int64    //Accumulated byte transfer from B to A
	udpClientMap                sync.Map  //map storing the UDP client-server connections
	parent                      *Manager  `json:"-"`

	//Custom fork (pool_custom.go, instances.go): per-upstream counters; poolMu
	//guards the exported fields and the stop channels, lifeMu serialises
	//Start and Stop, and listeners tracks the running listener goroutines
	pool      *upstreamPool
	poolOnce  sync.Once
	poolMu    sync.RWMutex
	lifeMu    sync.Mutex
	listeners *sync.WaitGroup
}

// InstanceView is a copy of a rule's settings and state (custom fork). It
// has the same fields as ProxyRelayInstance, so the saved config files and
// the API keep their format.
type InstanceView struct {
	UUID                 string
	Name                 string
	Running              bool
	AutoStart            bool
	ListeningAddress     string
	ProxyTargetAddr      string
	UseTCP               bool
	UseUDP               bool
	ProxyProtocolVersion ProxyProtocolVersion
	AccessRuleUUID       string
	EnableLogging        bool
	Timeout              int
	Destinations         []Destination
	Upstreams            []string
	LoadBalancing        string
	StickySessions       bool
	HealthCheck          *healthcheck.Config
	CloseOnUpstreamDown  bool
}

// View returns a copy of the rule's settings and state, taken under its lock.
func (c *ProxyRelayInstance) View() InstanceView {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	v := InstanceView{
		UUID:                 c.UUID,
		Name:                 c.Name,
		Running:              c.Running,
		AutoStart:            c.AutoStart,
		ListeningAddress:     c.ListeningAddress,
		ProxyTargetAddr:      c.ProxyTargetAddr,
		UseTCP:               c.UseTCP,
		UseUDP:               c.UseUDP,
		ProxyProtocolVersion: c.ProxyProtocolVersion,
		AccessRuleUUID:       c.AccessRuleUUID,
		EnableLogging:        c.EnableLogging,
		Timeout:              c.Timeout,
		LoadBalancing:        c.LoadBalancing,
		StickySessions:       c.StickySessions,
		CloseOnUpstreamDown:  c.CloseOnUpstreamDown,
	}
	if c.Destinations != nil {
		v.Destinations = append([]Destination{}, c.Destinations...)
	}
	if c.Upstreams != nil {
		v.Upstreams = append([]string{}, c.Upstreams...)
	}
	if c.HealthCheck != nil {
		hc := *c.HealthCheck
		v.HealthCheck = &hc
	}
	return v
}

// MarshalJSON writes the rule's view, so encoding never races with changes.
func (c *ProxyRelayInstance) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.View())
}

type Options struct {
	DefaultTimeout       int
	AccessControlHandler func(net.Conn) bool
	AccessController     *access.Controller //Access controller for per-instance access rules
	ConfigStore          string             //Folder to store the config files, will be created if not exists
	Logger               *logger.Logger     //Logger for the Layer 4 proxy

	//Custom fork (pool_custom.go): passive health monitoring of upstream
	//connection failures, and a callback after rules change
	ObserveConnect func(ruleID string, upstreamAddr string, err error)
	OnChange       func()
}

type Manager struct {
	//Config and stores
	Options *Options
	Configs []*ProxyRelayInstance

	//Custom fork: cfgMu guards Configs, saveMu serialises config file writes
	cfgMu  sync.RWMutex
	saveMu sync.Mutex

	//Custom fork: upstreams health checks took out of load balancing
	healthMu sync.Mutex
	offline  map[string]bool
}

// NewL4Proxy creates a new Layer 4 proxy manager with the given options
func NewL4Proxy(options *Options) (*Manager, error) {
	if !utils.FileExists(options.ConfigStore) {
		err := os.MkdirAll(options.ConfigStore, 0775)
		if err != nil {
			return nil, err
		}
	}

	//Load relay configs from db
	previousRules := []*ProxyRelayInstance{}
	convertedRules := false
	l4ProxyConfigFiles, err := filepath.Glob(options.ConfigStore + "/*.config")
	if err != nil {
		return nil, err
	}

	for _, configFile := range l4ProxyConfigFiles {
		//Read file into bytes
		configBytes, err := os.ReadFile(configFile)
		if err != nil {
			options.Logger.PrintAndLog("l4-proxy", "Read Layer 4 rule config failed", err)
			continue
		}
		thisRelayConfig := &ProxyRelayInstance{}
		err = json.Unmarshal(configBytes, thisRelayConfig)
		if err != nil {
			options.Logger.PrintAndLog("l4-proxy", "Unmarshal Layer 4 rule config failed", err)
			continue
		}

		// Check if UUID is empty
		if thisRelayConfig.UUID == "" {
			// Maybe copied from online tutorial without uuid, or the uuid field is accidentally deleted.
			// Generate a new UUID and save it back to file
			thisRelayConfig.UUID = uuid.New().String()
			newConfigBytes, err := json.Marshal(thisRelayConfig)
			if err != nil {
				options.Logger.PrintAndLog("l4-proxy", "Marshal Layer 4 rule config failed", err)
				continue
			}
			err = writeFileAtomic(configFile, newConfigBytes)
			if err != nil {
				options.Logger.PrintAndLog("l4-proxy", "Save Layer 4 rule config failed", err)
				continue
			}

			// Rename the file to uuid.config
			newConfigFile := filepath.Join(options.ConfigStore, thisRelayConfig.UUID+".config")
			err = os.Rename(configFile, newConfigFile)
			if err != nil {
				options.Logger.PrintAndLog("l4-proxy", "Rename Layer 4 rule config file failed", err)
				continue
			}
			options.Logger.PrintAndLog("l4-proxy", "Updated Layer 4 rule config with new UUID: "+thisRelayConfig.UUID, nil)
		}

		//Custom fork: rules from 2.9.1 and earlier get destinations with weights (pool_custom.go)
		if thisRelayConfig.normalizePool() {
			convertedRules = true
		}

		//Append the config to the list
		previousRules = append(previousRules, thisRelayConfig)
	}

	//Check if the AccessControlHandler is empty. If yes, set it to always allow access
	if options.AccessControlHandler == nil {
		options.AccessControlHandler = func(conn net.Conn) bool {
			//Always allow access
			return true
		}
	}

	//Create a new proxy manager for TCP
	thisManager := &Manager{
		Options: options,
		Configs: previousRules,
	}

	//Inject manager into the rules
	for _, rule := range previousRules {
		rule.parent = thisManager
	}
	if convertedRules {
		thisManager.SaveConfigToDatabase()
	}
	for _, rule := range previousRules {
		if rule.Running {
			//This was previously running. Start it again
			rule.Running = false
			thisManager.logf("Resuming Layer 4 rule "+rule.Name, nil)
			rule.Start()
		}
	}

	return thisManager, nil
}

// Wrapper function to log error
func (m *Manager) logf(message string, originalError error) {
	if m.Options.Logger == nil {
		//Print to fmt
		if originalError != nil {
			message += ": " + originalError.Error()
		}
		println(message)
		return
	}
	m.Options.Logger.PrintAndLog("l4-proxy", message, originalError)
}

// NewConfig creates a new proxy relay config with the given options
func (m *Manager) NewConfig(config *ProxyRelayOptions) string {
	//Generate two zero value for int64
	aAcc := int64(0)
	bAcc := int64(0)
	//Generate a new config from options
	configUUID := uuid.New().String()
	thisConfig := ProxyRelayInstance{
		UUID:                        configUUID,
		Name:                        config.Name,
		ListeningAddress:            config.ListeningAddr,
		ProxyTargetAddr:             config.ProxyAddr,
		Upstreams:                   []string{config.ProxyAddr}, //Custom fork: pool_custom.go
		Destinations:                []Destination{{Address: config.ProxyAddr, Weight: 1, Enabled: true}},
		LoadBalancing:               LBWeighted,
		UseTCP:                      config.UseTCP,
		UseUDP:                      config.UseUDP,
		ProxyProtocolVersion:        config.ProxyProtocolVersion,
		EnableLogging:               config.EnableLogging,
		Timeout:                     config.Timeout,
		AccessRuleUUID:              config.AccessRuleUUID,
		tcpStopChan:                 nil,
		udpStopChan:                 nil,
		aTobAccumulatedByteTransfer: &aAcc,
		bToaAccumulatedByteTransfer: &bAcc,
		udpClientMap:                sync.Map{},
		parent:                      m,
	}
	m.cfgMu.Lock()
	m.Configs = append(m.Configs, &thisConfig)
	m.cfgMu.Unlock()
	m.SaveConfigToDatabase()
	m.changed() //Custom fork
	return configUUID
}

// List returns the rules (custom fork). The slice is a copy; read a rule's
// settings with View.
func (m *Manager) List() []*ProxyRelayInstance {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return append([]*ProxyRelayInstance{}, m.Configs...)
}

func (m *Manager) GetConfigByUUID(configUUID string) (*ProxyRelayInstance, error) {
	// Find and return the config with the specified UUID
	for _, config := range m.List() {
		if config.UUID == configUUID {
			return config, nil
		}
	}
	return nil, errors.New("config not found")
}

// ConvertIntToProxyProtocolVersion converts an int to ProxyProtocolVersion type
func convertIntToProxyProtocolVersion(v int) ProxyProtocolVersion {
	switch v {
	case 1:
		return ProxyProtocolV1
	case 2:
		return ProxyProtocolV2
	default:
		return ProxyProtocolDisabled
	}
}

// convertProxyProtocolVersionToInt converts ProxyProtocolVersion type back to int
func convertProxyProtocolVersionToInt(v ProxyProtocolVersion) int {
	switch v {
	case ProxyProtocolV1:
		return 1
	case ProxyProtocolV2:
		return 2
	default:
		return 0
	}
}

// Edit the config based on config UUID, leave empty for unchange fields
func (m *Manager) EditConfig(newConfig *ProxyRuleUpdateConfig) error {
	// Find the config with the specified UUID
	foundConfig, err := m.GetConfigByUUID(newConfig.InstanceUUID)
	if err != nil {
		return err
	}

	if newConfig.NewAccessRuleUUID != "" {
		// Validate that the access rule exists; if not, fall back to default
		if m.Options.AccessController != nil && !m.Options.AccessController.AccessRuleExists(newConfig.NewAccessRuleUUID) {
			return errors.New("access rule not found: " + newConfig.NewAccessRuleUUID)
		}
	}
	if newConfig.NewTimeout != -1 && newConfig.NewTimeout < 0 {
		return errors.New("invalid timeout value given")
	}

	//Custom fork: stop a running rule before changing it (connections that
	//are open keep going), so its listeners never see half-changed settings
	wasRunning := foundConfig.IsRunning()
	if wasRunning {
		foundConfig.Stop()
	}

	// Validate and update the fields
	foundConfig.poolMu.Lock()
	if newConfig.NewName != "" {
		foundConfig.Name = newConfig.NewName
	}
	if newConfig.NewListeningAddr != "" {
		foundConfig.ListeningAddress = newConfig.NewListeningAddr
	}
	if newConfig.NewProxyAddr != "" {
		foundConfig.ProxyTargetAddr = newConfig.NewProxyAddr
		if len(foundConfig.Destinations) <= 1 { //Custom fork: a single destination follows the target address
			foundConfig.Upstreams = []string{newConfig.NewProxyAddr}
			d := Destination{Address: newConfig.NewProxyAddr, Weight: 1, Enabled: true}
			if len(foundConfig.Destinations) == 1 {
				d.Weight, d.Enabled = foundConfig.Destinations[0].Weight, foundConfig.Destinations[0].Enabled
			}
			foundConfig.Destinations = []Destination{d}
		}
		foundConfig.syncAddresses()
	}

	foundConfig.UseTCP = newConfig.UseTCP
	foundConfig.UseUDP = newConfig.UseUDP
	foundConfig.ProxyProtocolVersion = convertIntToProxyProtocolVersion(newConfig.ProxyProtocolVersion)
	foundConfig.EnableLogging = newConfig.EnableLogging

	if newConfig.NewAccessRuleUUID != "" {
		foundConfig.AccessRuleUUID = newConfig.NewAccessRuleUUID
	}
	if newConfig.NewTimeout != -1 {
		foundConfig.Timeout = newConfig.NewTimeout
	}
	foundConfig.poolMu.Unlock()

	m.SaveConfigToDatabase()
	m.changed() //Custom fork

	//Start it again if it was running
	if wasRunning {
		return foundConfig.Start()
	}
	return nil
}

// Remove the config from file by UUID
func (m *Manager) RemoveConfig(configUUID string) error {
	m.cfgMu.Lock()
	idx := -1
	for i, config := range m.Configs {
		if config.UUID == configUUID {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.cfgMu.Unlock()
		return errors.New("config not found")
	}
	m.Configs = append(m.Configs[:idx:idx], m.Configs[idx+1:]...)
	m.cfgMu.Unlock()

	//Custom fork: remove the file under saveMu, so a save running at the
	//same time cannot write it back
	m.saveMu.Lock()
	err := os.Remove(filepath.Join(m.Options.ConfigStore, configUUID+".config"))
	m.saveMu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	m.changed() //Custom fork
	return nil
}

// Save all configs to ConfigStore folder
func (m *Manager) SaveConfigToDatabase() {
	//Custom fork: one save at a time, each file replaced atomically (a crash
	//or a concurrent save used to be able to leave a truncated file), and
	//only files that changed are written
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	for _, config := range m.List() {
		configBytes, err := json.Marshal(config)
		if err != nil {
			m.logf("Failed to marshal Layer 4 rule config", err)
			continue
		}
		path := filepath.Join(m.Options.ConfigStore, config.UUID+".config")
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, configBytes) {
			continue
		}
		if err := writeFileAtomic(path, configBytes); err != nil {
			m.logf("Failed to save Layer 4 rule config", err)
		}
	}
}

// writeFileAtomic replaces path durably (utils.WriteFileAtomic).
func writeFileAtomic(path string, data []byte) error {
	return utils.WriteFileAtomic(path, data, 0640)
}

// getEffectiveAccessRule returns the access rule for this proxy instance.
// If the configured rule UUID is empty or no longer exists, it falls back to
// the default access rule. Returns nil if no AccessController is available.
func (c *ProxyRelayInstance) getEffectiveAccessRule() *access.AccessRule {
	if c.parent.Options.AccessController == nil {
		return nil
	}
	c.poolMu.RLock()
	ruleID, name := c.AccessRuleUUID, c.Name
	c.poolMu.RUnlock()
	rule, err := c.parent.Options.AccessController.GetAccessRuleByID(ruleID)
	if err != nil {
		// Rule not found (deleted or empty UUID); fall back to default
		c.parent.logf("Access rule \""+ruleID+"\" not found for Layer 4 rule "+name+", falling back to default", nil)
		rule, err = c.parent.Options.AccessController.GetAccessRuleByID("default")
		if err != nil {
			return nil
		}
	}
	return rule
}
