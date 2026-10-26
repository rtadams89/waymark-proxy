package access

/*
	store.go (custom fork): loading and saving ACLs and objects.

	conf/acl/<id>.json   one ACL each
	conf/acl/objects.json all objects

	Every save writes the whole ACL (or object list) atomically and bumps its
	revision; a change based on an older revision is refused, so two
	browser tabs cannot overwrite each other's changes.
*/

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

const objectsFile = "objects.json"

// ErrConflict is returned when a change is based on an older revision.
var ErrConflict = errors.New("this was changed elsewhere since you opened it; reload and make the change again")

// NewAccessController loads the ACLs, objects and trusted proxies.
func NewAccessController(options *Options) (*Controller, error) {
	if options.Database == nil {
		return nil, errors.New("missing database access")
	}
	if err := os.MkdirAll(options.ConfigFolder, 0775); err != nil {
		return nil, err
	}
	c := &Controller{Options: options, acls: map[string]*ACL{}, objects: map[string]*Object{}}

	if options.LegacyFolder != "" {
		if err := c.convertLegacy(options.LegacyFolder); err != nil {
			options.Logger.PrintAndLog("access", "Could not convert the access rules in "+options.LegacyFolder+" to ACLs", err)
		}
	}
	if err := c.load(); err != nil {
		return nil, err
	}

	if err := c.LoadTrustedProxies(); err != nil {
		options.Logger.PrintAndLog("access", "Unable to load trusted proxies from file", err)
		return nil, err
	}

	if options.PublicIpCheckInterval <= 0 {
		options.PublicIpCheckInterval = 12 * 60 * 60 //12 hours
	}
	c.ServerPublicIP = "127.0.0.1"
	go func() {
		if err := c.UpdatePublicIP(); err != nil {
			options.Logger.PrintAndLog("access", "Unable to update public IP address", err)
		}
		c.StartPublicIPUpdater()
	}()
	return c, nil
}

func (c *Controller) Close() {
	c.StopPublicIPUpdater()
}

func (c *Controller) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects = map[string]*Object{}
	if b, err := os.ReadFile(filepath.Join(c.Options.ConfigFolder, objectsFile)); err == nil {
		var list []*Object
		if err := json.Unmarshal(b, &list); err != nil {
			return fmt.Errorf("%s is not valid: %w", objectsFile, err)
		}
		for _, o := range list {
			if o != nil && ValidID(o.ID) && o.ID != PrivateNetworksID {
				o.BuiltIn = false
				c.objects[o.ID] = o
			}
		}
	}
	c.acls = map[string]*ACL{}
	files, _ := filepath.Glob(filepath.Join(c.Options.ConfigFolder, "*.json"))
	for _, f := range files {
		if filepath.Base(f) == objectsFile {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		a := &ACL{}
		if err := json.Unmarshal(b, a); err != nil || !ValidID(a.ID) || a.ID == AllowAllID {
			c.Options.Logger.PrintAndLog("access", "Skipped the ACL file "+filepath.Base(f)+" (not a valid ACL)", err)
			continue
		}
		a.BuiltIn = false
		c.acls[a.ID] = a
	}
	c.compileLocked()
	return nil
}

// Reload reads the ACLs and objects from disk again.
func (c *Controller) Reload() error { return c.load() }

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Controller) objectsWithBuiltIns() map[string]*Object {
	out := map[string]*Object{PrivateNetworksID: builtInPrivateNetworks()}
	for k, v := range c.objects {
		out[k] = v
	}
	return out
}

func (c *Controller) saveACLLocked(a *ACL) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return utils.WriteFileAtomic(filepath.Join(c.Options.ConfigFolder, a.ID+".json"), append(b, '\n'), 0644)
}

func (c *Controller) saveObjectsLocked() error {
	list := make([]*Object, 0, len(c.objects))
	for _, o := range c.objects {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return utils.WriteFileAtomic(filepath.Join(c.Options.ConfigFolder, objectsFile), append(b, '\n'), 0644)
}

/* ACLs */

func copyACL(a *ACL) *ACL {
	n := *a
	n.Rules = append([]Rule{}, a.Rules...)
	return &n
}

// ListACLs returns every ACL, Allow all first, then by name.
func (c *Controller) ListACLs() []*ACL {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*ACL{builtInAllowAll()}
	rest := []*ACL{}
	for _, a := range c.acls {
		rest = append(rest, copyACL(a))
	}
	sort.Slice(rest, func(i, j int) bool { return strings.ToLower(rest[i].Name) < strings.ToLower(rest[j].Name) })
	return append(out, rest...)
}

// GetACL returns a copy of an ACL.
func (c *Controller) GetACL(id string) (*ACL, error) {
	if id == AllowAllID {
		return builtInAllowAll(), nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.acls[id]
	if !ok {
		return nil, errors.New("ACL not found")
	}
	return copyACL(a), nil
}

// CreateACL adds an empty ACL (everything denied until rules are added).
func (c *Controller) CreateACL(name, description string) (*ACL, error) {
	name, err := checkName(name)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.acls {
		if strings.EqualFold(a.Name, name) {
			return nil, fmt.Errorf("an ACL named %q already exists", name)
		}
	}
	a := &ACL{ID: newID(), Name: name, Description: strings.TrimSpace(description), Rules: []Rule{}, Revision: 1}
	if err := c.saveACLLocked(a); err != nil {
		return nil, err
	}
	c.acls[a.ID] = a
	c.compileLocked()
	return copyACL(a), nil
}

// PrepareACL checks an ACL as it would be saved (normalised rules, the
// revision it is based on) without saving it.
func (c *Controller) PrepareACL(in *ACL) (*ACL, error) {
	if in.ID == AllowAllID {
		return nil, errors.New("the Allow all ACL cannot be changed")
	}
	name, err := checkName(in.Name)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	cur, ok := c.acls[in.ID]
	if !ok {
		return nil, errors.New("ACL not found")
	}
	if in.Revision != cur.Revision {
		return nil, ErrConflict
	}
	for _, a := range c.acls {
		if a.ID != in.ID && strings.EqualFold(a.Name, name) {
			return nil, fmt.Errorf("an ACL named %q already exists", name)
		}
	}
	objs := c.objectsWithBuiltIns()
	rules, err := normalizeRules(in.Rules, func(id string) bool { _, ok := objs[id]; return ok }, newID)
	if err != nil {
		return nil, err
	}
	return &ACL{ID: in.ID, Name: name, Description: strings.TrimSpace(in.Description), Rules: rules, Revision: cur.Revision + 1}, nil
}

// SaveACL saves an ACL prepared by PrepareACL.
func (c *Controller) SaveACL(a *ACL) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.acls[a.ID]
	if !ok {
		return errors.New("ACL not found")
	}
	if a.Revision != cur.Revision+1 {
		return ErrConflict
	}
	if err := c.saveACLLocked(a); err != nil {
		return err
	}
	c.acls[a.ID] = copyACL(a)
	c.compileLocked()
	return nil
}

// DeleteACL removes an ACL (the caller checks it is not in use).
func (c *Controller) DeleteACL(id string) error {
	if id == AllowAllID {
		return errors.New("the Allow all ACL cannot be deleted")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.acls[id]; !ok {
		return errors.New("ACL not found")
	}
	if err := os.Remove(filepath.Join(c.Options.ConfigFolder, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(c.acls, id)
	for k := range c.hits {
		if strings.HasPrefix(k, id+"/") {
			delete(c.hits, k)
		}
	}
	c.compileLocked()
	return nil
}

/* Objects */

func copyObject(o *Object) *Object {
	n := *o
	n.Entries = append([]Entry{}, o.Entries...)
	return &n
}

// ListObjects returns every object, Private networks first, then by name.
func (c *Controller) ListObjects() []*Object {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*Object{builtInPrivateNetworks()}
	rest := []*Object{}
	for _, o := range c.objects {
		rest = append(rest, copyObject(o))
	}
	sort.Slice(rest, func(i, j int) bool { return strings.ToLower(rest[i].Name) < strings.ToLower(rest[j].Name) })
	return append(out, rest...)
}

// GetObject returns a copy of an object.
func (c *Controller) GetObject(id string) (*Object, error) {
	if id == PrivateNetworksID {
		return builtInPrivateNetworks(), nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	o, ok := c.objects[id]
	if !ok {
		return nil, errors.New("object not found")
	}
	return copyObject(o), nil
}

// PrepareObject checks an object as it would be saved; an empty ID makes
// a new one.
func (c *Controller) PrepareObject(in *Object) (*Object, error) {
	if in.ID == PrivateNetworksID {
		return nil, errors.New("the Private networks object cannot be changed")
	}
	name, err := checkName(in.Name)
	if err != nil {
		return nil, err
	}
	entries, err := normalizeEntries(in.Entries)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := &Object{ID: in.ID, Name: name, Description: strings.TrimSpace(in.Description), Entries: entries}
	if in.ID == "" {
		out.ID, out.Revision = newID(), 1
	} else {
		cur, ok := c.objects[in.ID]
		if !ok {
			return nil, errors.New("object not found")
		}
		if in.Revision != cur.Revision {
			return nil, ErrConflict
		}
		out.Revision = cur.Revision + 1
	}
	for _, o := range c.objectsWithBuiltIns() {
		if o.ID != out.ID && strings.EqualFold(o.Name, name) {
			return nil, fmt.Errorf("an object named %q already exists", name)
		}
	}
	return out, nil
}

// SaveObject saves an object prepared by PrepareObject.
func (c *Controller) SaveObject(o *Object) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.objects[o.ID]; ok {
		if o.Revision != cur.Revision+1 {
			return ErrConflict
		}
	} else if o.Revision != 1 {
		return errors.New("object not found")
	}
	prev, had := c.objects[o.ID]
	c.objects[o.ID] = copyObject(o)
	if err := c.saveObjectsLocked(); err != nil {
		if had {
			c.objects[o.ID] = prev
		} else {
			delete(c.objects, o.ID)
		}
		return err
	}
	c.compileLocked()
	return nil
}

// ObjectUsers lists the ACLs (by name) whose rules use an object.
func (c *Controller) ObjectUsers(id string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []string{}
	for _, a := range c.acls {
		for _, r := range a.Rules {
			if r.Source.Type == SourceObject && r.Source.Value == id {
				out = append(out, a.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// DeleteObject removes an object no ACL uses.
func (c *Controller) DeleteObject(id string) error {
	if id == PrivateNetworksID {
		return errors.New("the Private networks object cannot be deleted")
	}
	if users := c.ObjectUsers(id); len(users) > 0 {
		return fmt.Errorf("this object is used by %s; remove it from those rules first", strings.Join(users, ", "))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.objects[id]
	if !ok {
		return errors.New("object not found")
	}
	delete(c.objects, id)
	if err := c.saveObjectsLocked(); err != nil {
		c.objects[id] = prev
		return err
	}
	c.compileLocked()
	return nil
}

// ACLsUsingCountries lists ACLs (by name) with a rule that needs a country
// lookup (Geo-IP).
func (c *Controller) ACLsUsingCountries() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	objs := c.objectsWithBuiltIns()
	out := []string{}
	for _, a := range c.acls {
		for _, r := range a.Rules {
			if !r.Enabled {
				continue
			}
			uses := r.Source.Type == SourceCountry
			if r.Source.Type == SourceObject {
				if o := objs[r.Source.Value]; o != nil {
					for _, e := range o.Entries {
						if e.Type == SourceCountry {
							uses = true
							break
						}
					}
				}
			}
			if uses {
				out = append(out, a.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
