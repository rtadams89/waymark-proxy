package utils

import (
	"encoding/json"
	"errors"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Response related
func SendTextResponse(w http.ResponseWriter, msg string) {
	w.Write([]byte(msg))
}

// Send JSON response, with an extra json header
func SendJSONResponse(w http.ResponseWriter, json string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(json))
}

func SendErrorResponse(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	js, _ := json.Marshal(map[string]string{"error": errMsg})
	w.Write(js)
}

func SendOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("\"OK\""))
}

// Get GET parameter
func GetPara(r *http.Request, key string) (string, error) {
	// Get first value from the URL query
	value := r.URL.Query().Get(key)
	if len(value) == 0 {
		return "", errors.New("invalid " + key + " given")
	}
	return value, nil
}

// Get GET parameter as boolean, only accept "true" or "false" (case insensitive)
func GetBool(r *http.Request, key string) (bool, error) {
	x, err := GetPara(r, key)
	if err != nil {
		return false, err
	}

	// Convert to lowercase and trim spaces just once to compare
	switch strings.ToLower(strings.TrimSpace(x)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}

	return false, errors.New("invalid boolean given")
}

// Get POST parameter
func PostPara(r *http.Request, key string) (string, error) {
	// Try to parse the form
	if err := r.ParseForm(); err != nil {
		return "", err
	}
	// Get first value from the form
	x := r.Form.Get(key)
	if len(x) == 0 {
		return "", errors.New("invalid " + key + " given")
	}
	return x, nil
}

// Get POST parameter as time.Duration
func PostDuration(r *http.Request, key string) (*time.Duration, error) {
	// Try to parse the form
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	// Get first value from the form
	x := r.Form.Get(key)
	if len(x) == 0 {
		return nil, errors.New("invalid " + key + " given")
	}

	duration, err := time.ParseDuration(x)

	if err != nil {
		return nil, errors.Join(errors.New("invalid "+key+" duration"), err)
	}
	return &duration, nil
}

// Get POST parameter as boolean, only accept "true" or "false" (case insensitive)
func PostBool(r *http.Request, key string) (bool, error) {
	x, err := PostPara(r, key)
	if err != nil {
		return false, err
	}

	// Convert to lowercase and trim spaces just once to compare
	switch strings.ToLower(strings.TrimSpace(x)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}

	return false, errors.New("invalid boolean given")
}

// Get POST paramter as int
func PostInt(r *http.Request, key string) (int, error) {
	x, err := PostPara(r, key)
	if err != nil {
		return 0, err
	}

	x = strings.TrimSpace(x)
	rx, err := strconv.Atoi(x)
	if err != nil {
		return 0, err
	}

	return rx, nil
}

func FileExists(filename string) bool {
	_, err := os.Stat(filename)
	if err == nil {
		// File exists
		return true
	} else if errors.Is(err, os.ErrNotExist) {
		// File does not exist
		return false
	}
	// Some other error
	return false
}

func IsDir(path string) bool {
	if !FileExists(path) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		log.Fatal(err)
		return false
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		return true
	case mode.IsRegular():
		return false
	}
	return false
}

// Check if given string in a given slice
func StringInArray(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}

// Validate if the listening address is correct
func ValidateListeningAddress(address string) bool {
	// Check if the address starts with a colon, indicating it's just a port
	if strings.HasPrefix(address, ":") {
		return true
	}

	// Split the address into host and port parts
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Try to parse it as just a port
		if _, err := strconv.Atoi(address); err == nil {
			return false // It's just a port number
		}
		return false // It's an invalid address
	}

	// Check if the port part is a valid number
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}

	// Check if the host part is a valid IP address or empty (indicating any IP)
	if host != "" {
		if net.ParseIP(host) == nil {
			return false
		}
	}

	return true
}

// SanitizeLogContent escapes special HTML characters in log content.
func SanitizeLogContent(content string) string {
	return html.EscapeString(content)
}
