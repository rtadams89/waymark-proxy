package netutils

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
)

/*
	Basic network helpers: port checks and domain validation
*/

// Check if the given port is already used by another process
func CheckIfPortOccupied(portNumber int) bool {
	// Try to connect to the port to see if something is listening
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(portNumber), time.Second)
	if err != nil {
		// Could not connect, port is not occupied
		return false
	}
	conn.Close()
	// Successfully connected, port is occupied
	return true
}

// NormalizeDomain cleans and validates a domain string.
// - Trims spaces around the domain
// - Converts to lowercase
// - Removes trailing dot (FQDN canonicalization)
// - Checks that the domain conforms to standard rules:
//   - Each label ≤ 63 characters
//   - Only letters, digits, and hyphens
//   - Labels do not start or end with a hyphen
//   - Full domain ≤ 253 characters
//
// Returns an empty string if the domain is invalid.
func NormalizeDomain(d string) (string, error) {
	d = strings.TrimSpace(d)
	d = strings.ToLower(d)
	d = strings.TrimSuffix(d, ".")

	if len(d) == 0 {
		return "", errors.New("domain is empty")
	}
	if len(d) > 253 {
		return "", errors.New("domain exceeds 253 characters")
	}

	labels := strings.Split(d, ".")
	for index, label := range labels {
		if index == 0 {
			if len(label) == 1 && label == "*" {
				continue
			}
		}
		if len(label) == 0 {
			return "", errors.New("Domain '" + d + "' not valid: Empty label")
		}
		if len(label) > 63 {
			return "", errors.New("Domain not valid: label exceeds 63 characters")
		}

		for i, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
				return "", errors.New("Domain '" + d + "' not valid: Invalid character '" + string(r) + "' in label")
			}
			if (i == 0 || i == len(label)-1) && r == '-' {
				return "", errors.New("Domain '" + d + "' not valid: label '" + label + "' starts or ends with hyphen")
			}
		}
	}

	return d, nil
}
