package auth

import (
	"crypto/rand"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/sessions"
	db "imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

type AuthAgent struct {
	SessionName             string
	SessionStore            *sessions.CookieStore
	Database                *db.Database
	LoginRedirectionHandler func(http.ResponseWriter, *http.Request)
	Logger                  *logger.Logger
}

func NewAuthenticationAgent(sessionName string, key []byte, sysdb *db.Database, allowReg bool, systemLogger *logger.Logger, loginRedirectionHandler func(http.ResponseWriter, *http.Request)) *AuthAgent {
	store := sessions.NewCookieStore(key)
	store.Options.HttpOnly = true
	store.Options.SameSite = http.SameSiteLaxMode
	err := sysdb.NewTable("auth")
	if err != nil {
		systemLogger.Println("Failed to create auth database. Terminating.")
		panic(err)
	}

	newAuthAgent := AuthAgent{
		SessionName:             sessionName,
		SessionStore:            store,
		Database:                sysdb,
		LoginRedirectionHandler: loginRedirectionHandler,
		Logger:                  systemLogger,
	}

	return &newAuthAgent
}

func GetSessionKey(sysdb *db.Database, logger *logger.Logger) (string, error) {
	sysdb.NewTable("auth")
	sessionKey := ""
	if !sysdb.KeyExists("auth", "sessionkey") {
		key := make([]byte, 32)
		rand.Read(key)
		sessionKey = string(key)
		sysdb.Write("auth", "sessionkey", sessionKey)
		logger.PrintAndLog("auth", "New authentication session key generated", nil)
	} else {
		logger.PrintAndLog("auth", "Authentication session key loaded from database", nil)
		err := sysdb.Read("auth", "sessionkey", &sessionKey)
		if err != nil {
			return "", errors.New("database read error. Is the database file corrupted?")
		}
	}
	return sessionKey, nil
}

// HandleCheckAuth calls handler if the request is logged in, or the login
// redirection handler.
func (a *AuthAgent) HandleCheckAuth(w http.ResponseWriter, r *http.Request, handler func(http.ResponseWriter, *http.Request)) {
	if a.CheckAuth(r) {
		handler(w, r)
	} else {
		a.LoginRedirectionHandler(w, r)
	}
}

// HandleLogin logs in with the posted username and password.
func (a *AuthAgent) HandleLogin(w http.ResponseWriter, r *http.Request) {

	username, err := utils.PostPara(r, "username")
	if err != nil {
		a.Logger.PrintAndLog("auth", r.RemoteAddr+" trying to login with username: "+username, nil)
		utils.SendErrorResponse(w, "Username not defined or empty.")
		return
	}

	password, err := utils.PostPara(r, "password")
	if err != nil {
		utils.SendErrorResponse(w, "Password not defined or empty.")
		return
	}

	rememberme := false
	rmbme, _ := utils.PostPara(r, "rmbme")
	if rmbme == "true" {
		rememberme = true
	}

	passwordCorrect, rejectionReason := a.ValidateUsernameAndPasswordWithReason(username, password)
	if passwordCorrect {
		a.LoginUserByRequest(w, r, username, rememberme)

		a.Logger.PrintAndLog("auth", username+" logged in.", nil)
		utils.SendOK(w)
	} else {
		a.Logger.PrintAndLog("auth", username+" login request rejected: "+rejectionReason, nil)

		utils.SendErrorResponse(w, rejectionReason)
		return
	}
}

func (a *AuthAgent) ValidateUsernameAndPassword(username string, password string) bool {
	succ, _ := a.ValidateUsernameAndPasswordWithReason(username, password)
	return succ
}

func (a *AuthAgent) ValidateUsernameAndPasswordWithReason(username string, password string) (bool, string) {
	var passwordInDB string
	err := a.Database.Read("auth", "passhash/"+username, &passwordInDB)
	if err != nil {
		a.Logger.PrintAndLog("auth", username+" login with incorrect password", nil)
		return false, "Invalid username or password"
	}

	ok, upgrade := PasswordMatches(passwordInDB, password)
	if !ok {
		return false, "Invalid username or password"
	}
	if upgrade {
		// Replaces the old unsalted hash
		if newHash, err := HashPassword(password); err == nil {
			if err := a.Database.Write("auth", "passhash/"+username, newHash); err != nil {
				a.Logger.PrintAndLog("auth", "Could not upgrade the password hash of "+username, err)
			}
		}
	}
	return true, ""
}

// LoginUserByRequest creates a session for username.
func (a *AuthAgent) LoginUserByRequest(w http.ResponseWriter, r *http.Request, username string, rememberme bool) {
	session, _ := a.SessionStore.Get(r, a.SessionName)

	session.Values["authenticated"] = true
	session.Values["username"] = username
	session.Values["rememberMe"] = rememberme

	if rememberme {
		session.Options = newSessionOptions(r, 3600*24*7) //One week
	} else {
		session.Options = newSessionOptions(r, 3600*1) //One hour
	}
	session.Save(r, w)
}

func newSessionOptions(r *http.Request, maxAge int) *sessions.Options {
	return &sessions.Options{
		MaxAge:   maxAge,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	}
}

// HandleLogout logs out and replies OK.
func (a *AuthAgent) HandleLogout(w http.ResponseWriter, r *http.Request) {
	username, err := a.GetUserName(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if username != "" {
		a.Logger.PrintAndLog("auth", username+" logged out", nil)
	}
	err = a.Logout(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "Logout failed")
		return
	}

	utils.SendOK(w)
}

func (a *AuthAgent) Logout(w http.ResponseWriter, r *http.Request) error {
	session, err := a.SessionStore.Get(r, a.SessionName)
	if err != nil {
		return err
	}
	session.Values["authenticated"] = false
	session.Values["username"] = nil
	session.Options.MaxAge = -1
	return session.Save(r, w)
}

func (a *AuthAgent) GetUserName(w http.ResponseWriter, r *http.Request) (string, error) {
	if a.CheckAuth(r) {
		session, _ := a.SessionStore.Get(r, a.SessionName)
		return session.Values["username"].(string), nil
	} else {
		return "", errors.New("user not logged in")
	}
}

// CheckLogin returns whether the request is logged in.
func (a *AuthAgent) CheckLogin(w http.ResponseWriter, r *http.Request) {
	if a.CheckAuth(r) {
		utils.SendJSONResponse(w, "true")
	} else {
		utils.SendJSONResponse(w, "false")
	}
}

// HandleRegisterWithoutEmail creates a user from the posted username and
// password.
func (a *AuthAgent) HandleRegisterWithoutEmail(w http.ResponseWriter, r *http.Request, callback func(string, string)) {
	newusername, err := utils.PostPara(r, "username")
	if err != nil {
		utils.SendErrorResponse(w, "Missing 'username' paramter")
		return
	}

	password, err := utils.PostPara(r, "password")
	if err != nil {
		utils.SendErrorResponse(w, "Missing 'password' paramter")
		return
	}

	err = a.CreateUserAccount(newusername, password, "")
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	if callback != nil {
		callback(newusername, "")
	}

	utils.SendOK(w)
	a.Logger.PrintAndLog("auth", "Admin account created: "+newusername, nil)
}

// CheckAuth reports whether the request's session is authenticated.
func (a *AuthAgent) CheckAuth(r *http.Request) bool {
	session, err := a.SessionStore.Get(r, a.SessionName)
	if err != nil {
		return false
	}

	if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
		return false
	}
	return true
}

func (a *AuthAgent) GetUserCounts() int {
	entries, _ := a.Database.ListTable("auth")
	usercount := 0
	for _, keypairs := range entries {
		if strings.Contains(string(keypairs[0]), "passhash/") {
			usercount++
		}
	}

	if usercount == 0 {
		a.Logger.PrintAndLog("auth", "There are no user in the database", nil)
	}
	return usercount
}

func (a *AuthAgent) UserExists(username string) bool {
	userpasswordhash := ""
	err := a.Database.Read("auth", "passhash/"+username, &userpasswordhash)
	if err != nil || userpasswordhash == "" {
		return false
	}
	return true
}

// SetPassword replaces an existing user's password.
func (a *AuthAgent) SetPassword(username string, password string) error {
	if !a.UserExists(username) {
		return errors.New("user not found")
	}
	hashedPassword, err := HashPassword(password)
	if err != nil {
		return err
	}
	return a.Database.Write("auth", "passhash/"+username, hashedPassword)
}

func (a *AuthAgent) CreateUserAccount(newusername string, password string, email string) error {
	if a.UserExists(newusername) {
		return errors.New("user with same name already exists")
	}

	key := newusername
	hashedPassword, err := HashPassword(password)
	if err != nil {
		return err
	}
	err = a.Database.Write("auth", "passhash/"+key, hashedPassword)
	if err != nil {
		return err
	}

	if email != "" {
		err = a.Database.Write("auth", "email/"+key, email)
		if err != nil {
			return err
		}
	}

	return nil
}
