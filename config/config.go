package config

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed version
var version string

//go:embed name
var name string

type LogLevel string

const (
	Debug LogLevel = "debug"
	Info  LogLevel = "info"
	Warn  LogLevel = "warn"
	Error LogLevel = "error"
)

func GetVersion() string {
	return strings.TrimSpace(version)
}

func GetName() string {
	return strings.TrimSpace(name)
}

func GetLogLevel() LogLevel {
	if IsDebug() {
		return Debug
	}
	logLevel := os.Getenv("XUI_LOG_LEVEL")
	if logLevel == "" {
		return Info
	}
	return LogLevel(logLevel)
}

func IsDebug() bool {
	return os.Getenv("XUI_DEBUG") == "true"
}

// TrustProxy reports whether the panel is behind a trusted reverse proxy. When
// enabled, X-Real-IP / X-Forwarded-For are used to determine the client address
// (needed for correct rate limiting). Keep it disabled when the panel is exposed
// directly, otherwise clients could spoof their address.
func TrustProxy() bool {
	return os.Getenv("XUI_TRUST_PROXY") == "true"
}

// CookieSecure reports whether session cookies should carry the Secure flag.
// Defaults to true only when the panel itself terminates TLS.
func CookieSecure() bool {
	if v := os.Getenv("XUI_COOKIE_SECURE"); v != "" {
		return v == "true"
	}
	return false
}

func GetDBPath() string {
	return fmt.Sprintf("/etc/%s/%s.db", GetName(), GetName())
}
