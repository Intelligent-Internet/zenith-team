package util

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errStudioOrigin = errors.New("invalid studio origin")

// CanonicalStudioOrigin validates a credential origin without accepting API paths.
func CanonicalStudioOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(raw, "#") ||
		(u.Path != "" && u.Path != "/") ||
		u.RawPath != "" {
		return "", errStudioOrigin
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(u.Host, ":") {
		return "", errStudioOrigin
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil && ip.String() != host {
			return "", errStudioOrigin
		}
	} else {
		last := host[strings.LastIndex(host, ".")+1:]
		if last != "" && last[0] >= '0' && last[0] <= '9' {
			return "", errStudioOrigin
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", errStudioOrigin
			}
			for _, r := range label {
				if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
					return "", errStudioOrigin
				}
			}
		}
	}
	if strings.ContainsAny(host, "%\\ \t\r\n") || strings.Trim(host, ".") != host || !utf8.ValidString(host) {
		return "", errStudioOrigin
	}
	for _, r := range host {
		if r > 127 {
			return "", errStudioOrigin
		}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && (scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1")) {
		return "", errStudioOrigin
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", errStudioOrigin
		}
	}
	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	if strings.Contains(host, ":") {
		if host != "::1" && net.ParseIP(host) == nil {
			return "", errStudioOrigin
		}
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}
