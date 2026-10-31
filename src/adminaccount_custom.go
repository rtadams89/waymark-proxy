package main

/*
	Admin accounts: the one-time setup code needed to create the first admin
	account in the web UI, and -set-admin-password, which sets an account's
	password (or creates the account) at the terminal.

	Without the setup code, whoever opened the admin console first on a new
	install could make themselves its admin. The code is written to the log
	when Waymark starts without an admin account, so only someone who can
	read the log on this machine can create the first account.
*/

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/database/dbbolt"
	"imuslab.com/zoraxy/mod/database/dbinc"
	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/utils"
)

// Crockford base32: no I, L, O or U, so the code is easy to read and type.
const setupCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const setupCodeLength = 12 // 60 bits

var adminSetup struct {
	sync.Mutex
	code string
}

// newSetupCode returns a random code formatted as XXXX-XXXX-XXXX.
func newSetupCode() string {
	b := make([]byte, setupCodeLength)
	if _, err := rand.Read(b); err != nil {
		panic("cannot read random bytes: " + err.Error())
	}
	var sb strings.Builder
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(setupCodeAlphabet[int(v)%len(setupCodeAlphabet)])
	}
	return sb.String()
}

// normalizeSetupCode uppercases s, drops spaces and dashes, and maps the
// letters people confuse with digits (O, I, L) to them.
func normalizeSetupCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '\t':
			return -1
		case 'O':
			return '0'
		case 'I', 'L':
			return '1'
		}
		return r
	}, s)
}

func setupCodeMatches(want, given string) bool {
	if want == "" {
		return false
	}
	w, g := normalizeSetupCode(want), normalizeSetupCode(given)
	return subtle.ConstantTimeCompare([]byte(w), []byte(g)) == 1
}

// logSetupCode logs how to create the first admin account. Callers hold
// adminSetup.
func logSetupCode() {
	SystemWideLogger.PrintAndLog("auth", "No admin account yet. To create one, open the admin console and enter the setup code "+adminSetup.code+" (it changes every time Waymark starts), or stop Waymark and run waymark -set-admin-password", nil)
}

// initAdminSetupCode creates and logs a setup code if there is no admin
// account.
func initAdminSetupCode() {
	adminSetup.Lock()
	defer adminSetup.Unlock()
	if len(auth.ListUsernames(sysdb)) == 0 {
		adminSetup.code = newSetupCode()
		logSetupCode()
	}
}

// HandleAdminRegister creates the first admin account from username,
// password and setupcode.
func HandleAdminRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminSetup.Lock()
	defer adminSetup.Unlock()
	if len(auth.ListUsernames(sysdb)) != 0 {
		utils.SendErrorResponse(w, "An admin account already exists")
		return
	}
	if adminSetup.code == "" {
		// The accounts were removed while Waymark was running
		adminSetup.code = newSetupCode()
		logSetupCode()
	}
	given, _ := utils.PostPara(r, "setupcode")
	if !setupCodeMatches(adminSetup.code, given) {
		SystemWideLogger.PrintAndLog("auth", "Admin account setup refused: wrong setup code from "+netutils.GetRequesterIP(r), nil)
		utils.SendErrorResponse(w, "The setup code is wrong. Waymark writes it to its log when it starts without an admin account.")
		return
	}
	username, _ := utils.PostPara(r, "username")
	password, _ := utils.PostPara(r, "password")
	if err := auth.ValidateUsername(username); err != nil {
		utils.SendErrorResponse(w, "Cannot create the account: "+err.Error())
		return
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		utils.SendErrorResponse(w, "Cannot create the account: "+err.Error())
		return
	}
	if err := authAgent.CreateUserAccount(username, password, ""); err != nil {
		utils.SendErrorResponse(w, "Cannot create the account: "+err.Error())
		return
	}
	adminSetup.code = ""
	SystemWideLogger.PrintAndLog("auth", "Admin account created: "+username+" (from "+netutils.GetRequesterIP(r)+")", nil)
	utils.SendOK(w)
}

// selectedDBBackend is the database backend chosen with -db.
func selectedDBBackend() dbinc.BackendType {
	switch *databaseBackend {
	case "leveldb":
		return dbinc.BackendLevelDB
	case "boltdb":
		return dbinc.BackendBoltDB
	}
	return database.GetRecommendedBackendType()
}

// passwordReader reads lines from the terminal, hiding what is typed for
// passwords when it can.
type passwordReader struct {
	in       *bufio.Reader
	out      io.Writer
	terminal bool
}

func (p *passwordReader) line(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	s, err := p.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (p *passwordReader) secret(prompt string) (string, error) {
	hidden := false
	if p.terminal {
		if err := sttyEcho(false); err == nil {
			hidden = true
			defer sttyEcho(true)
		}
	}
	s, err := p.line(prompt)
	if hidden {
		fmt.Fprintln(p.out)
	}
	return s, err
}

func sttyEcho(on bool) error {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// promptAdminPassword asks for the account and its new password.
func promptAdminPassword(p *passwordReader, existing []string) (string, string, error) {
	switch len(existing) {
	case 0:
		fmt.Fprintln(p.out, "There is no admin account yet. This creates one.")
	default:
		fmt.Fprintln(p.out, "Admin accounts: "+strings.Join(existing, ", ")+". Enter one of them to change its password, or a new name to add an account.")
	}
	prompt := "Username: "
	if len(existing) == 1 {
		prompt = "Username [" + existing[0] + "]: "
	}
	username, err := p.line(prompt)
	if err != nil {
		return "", "", err
	}
	if username == "" && len(existing) == 1 {
		username = existing[0]
	}
	if err := auth.ValidateUsername(username); err != nil {
		return "", "", err
	}
	password, err := p.secret("New password: ")
	if err != nil {
		return "", "", err
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		return "", "", err
	}
	again, err := p.secret("Confirm new password: ")
	if err != nil {
		return "", "", err
	}
	if again != password {
		return "", "", errors.New("the passwords do not match")
	}
	return username, password, nil
}

// setAdminPasswordAndExit runs -set-admin-password.
func setAdminPasswordAndExit() {
	fail := func(msg string) {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
	if !utils.FileExists(*path_database) {
		fail("No database at " + *path_database + ". Run this in Waymark's folder (cd /opt/waymark), as the user Waymark runs as, with the same -dbpath and -db as the service.")
	}
	dbbolt.OpenTimeout = 3 * time.Second
	db, err := database.NewDatabase(*path_database, selectedDBBackend())
	if err != nil {
		fail("Cannot open the database (" + err.Error() + "). Stop Waymark first: sudo systemctl stop waymark")
	}
	info, _ := os.Stdin.Stat()
	p := &passwordReader{in: bufio.NewReader(os.Stdin), out: os.Stdout, terminal: info != nil && info.Mode()&os.ModeCharDevice != 0}
	username, password, err := promptAdminPassword(p, auth.ListUsernames(db))
	if err != nil {
		db.Close()
		fail("Nothing was changed: " + err.Error())
	}
	created, err := auth.SetOrCreateAccount(db, username, password)
	if err != nil {
		db.Close()
		fail("Cannot save the account: " + err.Error())
	}
	if created {
		fmt.Println("Admin account " + username + " created.")
	} else {
		fmt.Println("Password changed for " + username + ".")
	}
	fmt.Println("Everyone signed in to the admin console will have to sign in again.")
	fmt.Println("Start Waymark again: sudo systemctl start waymark")
	fmt.Println("With HA, do this on the active node: the other nodes copy the admin accounts from it.")
	db.Close()
	os.Exit(0)
}
