package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"

	"github.com/go-acme/lego/v5/acme"
)

// Storage of ACME accounts (key and registration), reused per CA directory
// URL and email.

// acmeAccountTable is the sys.db table storing ACME accounts.
const acmeAccountTable = "acme"

// ACMEUser represents a user in the ACME system.
type ACMEUser struct {
	Email        string
	Registration *acme.ExtendedAccount
	key          crypto.Signer
}

func (u *ACMEUser) GetEmail() string {
	return u.Email
}

func (u *ACMEUser) GetRegistration() *acme.ExtendedAccount {
	return u.Registration
}

func (u *ACMEUser) GetPrivateKey() crypto.Signer {
	return u.key
}

// MarshalJSON serializes the private key as a PEM string.
func (u *ACMEUser) MarshalJSON() ([]byte, error) {
	pemData := ""
	if u.key == nil {
		return nil, errors.New("no ACME account key found")
	}
	ecKey, ok := u.key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("ACME account private key is not an ECDSA key")
	}

	der, _ := x509.MarshalECPrivateKey(ecKey)
	pemData = string(pem.EncodeToMemory(&pem.Block{
		Type: "EC PRIVATE KEY", Bytes: der,
	}))

	return json.Marshal(struct {
		Email        string                `json:"email"`
		Registration *acme.ExtendedAccount `json:"registration"`
		KeyPEM       string                `json:"key"`
	}{
		Email:        u.Email,
		Registration: u.Registration,
		KeyPEM:       pemData,
	})
}

// UnmarshalJSON deserializes the private key from its PEM string.
func (u *ACMEUser) UnmarshalJSON(data []byte) error {
	aux := struct {
		Email        string                `json:"email"`
		Registration *acme.ExtendedAccount `json:"registration"`
		KeyPEM       string                `json:"key"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	u.Email = aux.Email
	u.Registration = aux.Registration

	if aux.KeyPEM == "" {
		return errors.New("no ACME account key stored")
	}

	block, _ := pem.Decode([]byte(aux.KeyPEM))
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return errors.New("stored ACME account key could not be decoded as EC private key")
	}

	parsedKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return errors.New("stored ACME account key could not be parsed")
	}
	u.key = parsedKey

	return nil
}

// accountDBKey returns the database key for a CA directory URL and email.
func accountDBKey(caDirURL string, email string) string {
	h := sha256.Sum256([]byte(caDirURL + "|" + email))
	return "acme_account_" + hex.EncodeToString(h[:8])
}

// loadACMEAccount returns a stored account for the CA directory URL and
// email; ok is false when none is usable.
func (a *ACMEHandler) loadACMEAccount(caDirURL string, email string) (*ecdsa.PrivateKey, *acme.ExtendedAccount, bool) {
	if !a.Database.TableExists(acmeAccountTable) {
		return nil, nil, false
	}

	dbKey := accountDBKey(caDirURL, email)
	if !a.Database.KeyExists(acmeAccountTable, dbKey) {
		return nil, nil, false
	}

	var user ACMEUser
	if err := a.Database.Read(acmeAccountTable, dbKey, &user); err != nil {
		a.Logf("Stored ACME account for "+caDirURL+" is invalid, registering a new account", err)
		return nil, nil, false
	}

	if user.Registration == nil || user.Registration.Location == "" {
		return nil, nil, false
	}

	ecKey, ok := user.key.(*ecdsa.PrivateKey)
	if !ok {
		a.Logf("Stored ACME account key for "+caDirURL+" is not an ECDSA key", nil)
		return nil, nil, false
	}

	return ecKey, user.Registration, true
}

// saveACMEAccount stores the account key and registration.
func (a *ACMEHandler) saveACMEAccount(caDirURL string, user *ACMEUser) error {
	if user == nil || user.GetRegistration() == nil {
		return errors.New("no ACME registration to persist")
	}

	if !a.Database.TableExists(acmeAccountTable) {
		if err := a.Database.NewTable(acmeAccountTable); err != nil {
			return err
		}
	}

	return a.Database.Write(acmeAccountTable, accountDBKey(caDirURL, user.Email), user)
}
