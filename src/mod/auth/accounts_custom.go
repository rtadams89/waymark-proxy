package auth

// Admin account helpers that work on the database directly, for the
// -set-admin-password command (Waymark is stopped, so there is no AuthAgent).

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	db "imuslab.com/zoraxy/mod/database"
)

const maxUsernameLength = 64

// ValidateUsername returns an error if name cannot be an admin username.
func ValidateUsername(name string) error {
	if name == "" {
		return errors.New("the username is empty")
	}
	if utf8.RuneCountInString(name) > maxUsernameLength {
		return errors.New("the username is longer than 64 characters")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("the username starts or ends with a space")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' {
			return errors.New("the username contains a control character or /")
		}
	}
	return nil
}

// ValidateNewPassword returns an error if password cannot be set.
func ValidateNewPassword(password string) error {
	if password == "" {
		return errors.New("the password is empty")
	}
	if len(password) > 72 {
		return errors.New("passwords can be at most 72 bytes long")
	}
	return nil
}

// ListUsernames returns the admin account names, sorted.
func ListUsernames(sysdb *db.Database) []string {
	names := []string{}
	if !sysdb.TableExists("auth") {
		return names
	}
	entries, _ := sysdb.ListTable("auth")
	for _, kv := range entries {
		if name, ok := strings.CutPrefix(string(kv[0]), "passhash/"); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// SetOrCreateAccount sets username's password, creating the account if it
// does not exist (created is true then). It also replaces the session key,
// so everyone signed in to the admin console has to sign in again.
func SetOrCreateAccount(sysdb *db.Database, username, password string) (created bool, err error) {
	if err := ValidateUsername(username); err != nil {
		return false, err
	}
	if err := ValidateNewPassword(password); err != nil {
		return false, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	if err := sysdb.NewTable("auth"); err != nil {
		return false, err
	}
	created = !sysdb.KeyExists("auth", "passhash/"+username)
	if err := sysdb.Write("auth", "passhash/"+username, hash); err != nil {
		return false, err
	}
	if sysdb.KeyExists("auth", "sessionkey") {
		if err := sysdb.Delete("auth", "sessionkey"); err != nil {
			return created, errors.New("the password was set, but signed-in sessions could not be ended: " + err.Error())
		}
	}
	return created, nil
}
