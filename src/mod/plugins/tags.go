package plugins

import (
	"encoding/json"
	"imuslab.com/zoraxy/mod/utils"
	"os"
)

// Plugin tags match plugins to HTTP proxy rules with the same tag.

// LoadPluginGroupsFromConfig loads the plugin tags into option.PluginGroups;
// call UpdateTagsToPluginMaps() to apply them.
func (m *Manager) LoadPluginGroupsFromConfig() error {
	m.pluginGroupsMutex.RLock()
	defer m.pluginGroupsMutex.RUnlock()

	//Read the config file
	rawConfig, err := os.ReadFile(m.Options.PluginGroupsConfig)
	if err != nil {
		return err
	}

	var config map[string][]string
	err = json.Unmarshal(rawConfig, &config)
	if err != nil {
		return err
	}

	m.Options.PluginGroups = config
	return nil
}

// AddPluginToTag adds a plugin to a tag
func (m *Manager) AddPluginToTag(tag string, pluginID string) error {
	m.pluginGroupsMutex.RLock()
	defer m.pluginGroupsMutex.RUnlock()

	//Check if the plugin exists
	_, err := m.GetPluginByID(pluginID)
	if err != nil {
		return err
	}

	//Add to m.Options.PluginGroups
	pluginList, ok := m.Options.PluginGroups[tag]
	if !ok {
		pluginList = []string{}
	}
	pluginList = append(pluginList, pluginID)
	m.Options.PluginGroups[tag] = pluginList

	//Update to runtime
	m.UpdateTagsToPluginMaps()

	//Save to file
	return m.savePluginTagMap()
}

// RemovePluginFromTag removes a plugin from a tag
func (m *Manager) RemovePluginFromTag(tag string, pluginID string) error {
	// Check if the plugin exists in Options.PluginGroups
	m.pluginGroupsMutex.RLock()
	defer m.pluginGroupsMutex.RUnlock()
	pluginList, ok := m.Options.PluginGroups[tag]
	if !ok {
		return nil
	}

	// Remove the plugin from the list
	for i, id := range pluginList {
		if id == pluginID {
			pluginList = append(pluginList[:i], pluginList[i+1:]...)
			break
		}
	}
	m.Options.PluginGroups[tag] = pluginList

	// Update to runtime
	m.UpdateTagsToPluginMaps()

	// Save to file
	return m.savePluginTagMap()
}

// savePluginTagMap saves the plugin tag map to the config file
func (m *Manager) savePluginTagMap() error {
	m.pluginGroupsMutex.RLock()
	defer m.pluginGroupsMutex.RUnlock()

	js, _ := json.Marshal(m.Options.PluginGroups)
	return utils.WriteFileAtomic(m.Options.PluginGroupsConfig, js, 0644)
}
