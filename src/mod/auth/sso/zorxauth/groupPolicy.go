package zorxauth

// Group policies of Waymark Auth, stored as JSON files in the config folder.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	GROUP_POLICY_FOLDER   = "group_policies"
	GROUP_POLICY_FILE_EXT = ".json"
)

// GroupPolicy defines a group-level access policy that can be shared across users
type GroupPolicy struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	AllowedHosts []string `json:"allowedHosts"` // empty allows all hosts
}

// initGroupPolicyStore ensures the storage folder exists and loads policies from disk into sync.Map
func (ar *AuthRouter) initGroupPolicyStore() {
	folder := ar.groupPolicyFolder()
	if err := os.MkdirAll(folder, 0755); err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to create group policy folder: "+err.Error(), err)
		}
		return
	}

	ar.loadGroupPoliciesFromDisk()
}

func (ar *AuthRouter) groupPolicyFolder() string {
	base := ar.Options.ConfigFolderPath
	if base == "" {
		base = "./conf/sso/zorxauth"
	}
	return filepath.Join(base, GROUP_POLICY_FOLDER)
}

// groupPolicyIDPattern matches the IDs that can be used as file names.
var groupPolicyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validGroupPolicyID(id string) bool { return groupPolicyIDPattern.MatchString(id) }

func (ar *AuthRouter) groupPolicyFilePath(id string) string {
	return filepath.Join(ar.groupPolicyFolder(), id+GROUP_POLICY_FILE_EXT)
}

// loadGroupPoliciesFromDisk reads all .json files from the policy folder into the sync.Map
func (ar *AuthRouter) loadGroupPoliciesFromDisk() {
	folder := ar.groupPolicyFolder()
	entries, err := os.ReadDir(folder)
	if err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to read group policy folder: "+err.Error(), err)
		}
		return
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), GROUP_POLICY_FILE_EXT) {
			continue
		}

		data, err := os.ReadFile(filepath.Join(folder, entry.Name()))
		if err != nil {
			if ar.Logger != nil {
				ar.Logger.PrintAndLog("waymark-auth", "Failed to read group policy file "+entry.Name()+": "+err.Error(), err)
			}
			continue
		}

		var gp GroupPolicy
		if err := json.Unmarshal(data, &gp); err != nil {
			if ar.Logger != nil {
				ar.Logger.PrintAndLog("waymark-auth", "Failed to parse group policy file "+entry.Name()+": "+err.Error(), err)
			}
			continue
		}

		if !validGroupPolicyID(gp.ID) {
			if ar.Logger != nil {
				ar.Logger.PrintAndLog("waymark-auth", "Skipped group policy file "+entry.Name()+": invalid ID", nil)
			}
			continue
		}

		ar.groupPolicies.Store(gp.ID, &gp)
		loaded++
	}

	if ar.Logger != nil {
		ar.Logger.PrintAndLog("waymark-auth", "Loaded "+strconv.Itoa(loaded)+" group policies from disk", nil)
	}
}

func (ar *AuthRouter) saveGroupPolicyToDisk(gp *GroupPolicy) error {
	if !validGroupPolicyID(gp.ID) {
		return errors.New("invalid group policy ID")
	}
	folder := ar.groupPolicyFolder()
	if err := os.MkdirAll(folder, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(gp, "", "  ")
	if err != nil {
		return err
	}

	return utils.WriteFileAtomic(ar.groupPolicyFilePath(gp.ID), data, 0644)
}

func (ar *AuthRouter) deleteGroupPolicyFromDisk(id string) error {
	if !validGroupPolicyID(id) {
		return errors.New("invalid group policy ID")
	}
	fp := ar.groupPolicyFilePath(id)
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(fp)
}

// GetGroupPolicyByID returns a copy of the group policy with the given ID
func (ar *AuthRouter) GetGroupPolicyByID(id string) (*GroupPolicy, error) {
	val, exists := ar.groupPolicies.Load(id)
	if !exists {
		return nil, errors.New("group policy not found")
	}

	gp, ok := val.(*GroupPolicy)
	if !ok {
		return nil, errors.New("invalid group policy entry")
	}

	gpCopy := *gp
	gpCopy.AllowedHosts = make([]string, len(gp.AllowedHosts))
	copy(gpCopy.AllowedHosts, gp.AllowedHosts)
	return &gpCopy, nil
}

// ListGroupPolicies returns all group policies sorted by name
func (ar *AuthRouter) ListGroupPolicies() []*GroupPolicy {
	var result []*GroupPolicy
	ar.groupPolicies.Range(func(key, value interface{}) bool {
		gp, ok := value.(*GroupPolicy)
		if !ok {
			return true
		}
		gpCopy := *gp
		gpCopy.AllowedHosts = make([]string, len(gp.AllowedHosts))
		copy(gpCopy.AllowedHosts, gp.AllowedHosts)
		result = append(result, &gpCopy)
		return true
	})

	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	return result
}

// GetAllowedHostsForUser returns the group policy's allowed hosts when the
// user uses one, otherwise the user's own.
func (ar *AuthRouter) GetAllowedHostsForUser(user *User) []string {
	if user.UseGroupPolicy && user.GroupID != "" {
		gp, err := ar.GetGroupPolicyByID(user.GroupID)
		if err == nil {
			return gp.AllowedHosts
		}
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Group policy "+user.GroupID+" not found for user "+user.Username+", falling back to user-level hosts", nil)
		}
	}
	return user.AllowedHosts
}

/* ===================== HTTP API Handlers ===================== */

// HandleGroupPolicyList returns all group policies as JSON (GET)
func (ar *AuthRouter) HandleGroupPolicyList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.SendErrorResponse(w, "method not allowed")
		return
	}

	policies := ar.ListGroupPolicies()
	js, _ := json.Marshal(policies)
	utils.SendJSONResponse(w, string(js))
}

// HandleGroupPolicyCreate creates a new group policy (POST)
func (ar *AuthRouter) HandleGroupPolicyCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "method not allowed")
		return
	}

	name, err := utils.PostPara(r, "name")
	if err != nil || strings.TrimSpace(name) == "" {
		utils.SendErrorResponse(w, "group policy name is required")
		return
	}

	description := strings.TrimSpace(r.FormValue("description"))
	allowedHosts := normalizeAllowedHosts(r.FormValue("allowedHosts"))

	gp := &GroupPolicy{
		ID:           uuid.NewString(),
		Name:         strings.TrimSpace(name),
		Description:  description,
		AllowedHosts: allowedHosts,
	}

	ar.groupPolicies.Store(gp.ID, gp)

	if err := ar.saveGroupPolicyToDisk(gp); err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to save group policy to disk: "+err.Error(), err)
		}
		utils.SendErrorResponse(w, "failed to persist group policy")
		return
	}

	utils.SendOK(w)
}

// HandleGroupPolicyUpdate updates an existing group policy (POST)
func (ar *AuthRouter) HandleGroupPolicyUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "method not allowed")
		return
	}

	id, err := utils.PostPara(r, "id")
	if err != nil || strings.TrimSpace(id) == "" {
		utils.SendErrorResponse(w, "group policy ID is required")
		return
	}

	val, exists := ar.groupPolicies.Load(id)
	if !exists {
		utils.SendErrorResponse(w, "group policy not found")
		return
	}

	oldGP, ok := val.(*GroupPolicy)
	if !ok {
		utils.SendErrorResponse(w, "invalid group policy entry")
		return
	}

	updated := *oldGP
	updated.AllowedHosts = make([]string, len(oldGP.AllowedHosts))
	copy(updated.AllowedHosts, oldGP.AllowedHosts)

	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		updated.Name = name
	}

	if _, ok := r.Form["description"]; ok {
		updated.Description = strings.TrimSpace(r.FormValue("description"))
	}

	if _, ok := r.Form["allowedHosts"]; ok {
		updated.AllowedHosts = normalizeAllowedHosts(r.FormValue("allowedHosts"))
	}

	ar.groupPolicies.Store(id, &updated)

	if err := ar.saveGroupPolicyToDisk(&updated); err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to save group policy to disk: "+err.Error(), err)
		}
		utils.SendErrorResponse(w, "failed to persist group policy")
		return
	}

	utils.SendOK(w)
}

// HandleGroupPolicyDelete deletes a group policy (POST)
func (ar *AuthRouter) HandleGroupPolicyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "method not allowed")
		return
	}

	id, err := utils.PostPara(r, "id")
	if err != nil || strings.TrimSpace(id) == "" {
		utils.SendErrorResponse(w, "group policy ID is required")
		return
	}

	if _, exists := ar.groupPolicies.Load(id); !exists {
		utils.SendErrorResponse(w, "group policy not found")
		return
	}

	ar.groupPolicies.Delete(id)

	if err := ar.deleteGroupPolicyFromDisk(id); err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to delete group policy file: "+err.Error(), err)
		}
	}

	users, err := ar.listUsers()
	if err == nil {
		for _, user := range users {
			if user.UseGroupPolicy && strings.EqualFold(user.GroupID, id) {
				user.UseGroupPolicy = false
				user.GroupID = ""
				if saveErr := ar.saveUser(user, ""); saveErr != nil {
					if ar.Logger != nil {
						ar.Logger.PrintAndLog("waymark-auth", "Failed to unassign group policy from user "+user.Username+": "+saveErr.Error(), saveErr)
					}
				}
			}
		}
	}

	utils.SendOK(w)
}
