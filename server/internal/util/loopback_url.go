package util

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeLoopbackHTTPURL validates a local development-server URL and
// returns a stable representation suitable for a persisted preview target.
func NormalizeLoopbackHTTPURL(value string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" {
		return "", errors.New("URL must be an HTTP loopback URL with an explicit port")
	}
	hostname := strings.ToLower(parsed.Hostname())
	address := net.ParseIP(hostname)
	if hostname != "localhost" && !strings.HasSuffix(hostname, ".localhost") &&
		(address == nil || !address.IsLoopback()) {
		return "", errors.New("URL must be an HTTP loopback URL with an explicit port")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("URL must be an HTTP loopback URL with an explicit port")
	}
	parsed.Host = net.JoinHostPort(hostname, strconv.Itoa(port))
	return parsed.String(), nil
}
