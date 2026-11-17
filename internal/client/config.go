package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Endpoint describes one SSH endpoint: either the target host or a single jump
// host. Secrets are carried as plain strings so a Connection Profile can be
// persisted by Terraform and reloaded in a later process.
type Endpoint struct {
	Host                  string
	Port                  string
	User                  string
	PrivateKey            string
	PrivateKeyPassphrase  string
	KnownHosts            string
	HostKey               string
	InsecureIgnoreHostKey bool
}

// Config is the complete connection configuration used to dial the target.
type Config struct {
	Endpoint
	Envs       map[string]string
	Timeout    time.Duration
	RetryDelay time.Duration
	Jump       *Endpoint
}

// DefaultPort is used when a Connection Profile leaves the port empty.
const DefaultPort = "22"

// DefaultKnownHostsPath is only consulted when no host key mode was configured.
func DefaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for default known_hosts: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// ValidateEndpoint performs configuration-only validation. It does not touch
// the network.
func ValidateEndpoint(ep Endpoint, allowJump bool) error {
	var problems []string
	if strings.TrimSpace(ep.Host) == "" {
		problems = append(problems, "host must not be empty")
	}
	if strings.ContainsAny(ep.Host, "\r\n\x00") {
		problems = append(problems, "host contains invalid characters")
	}
	if ep.Port == "" {
		problems = append(problems, "port must not be empty")
	} else if p, err := strconv.Atoi(ep.Port); err != nil || p < 1 || p > 65535 {
		problems = append(problems, "port must be an integer between 1 and 65535")
	}
	if strings.TrimSpace(ep.User) == "" {
		problems = append(problems, "user must not be empty")
	}
	if strings.ContainsAny(ep.User, "\r\n\x00") {
		problems = append(problems, "user contains invalid characters")
	}
	if strings.TrimSpace(ep.PrivateKey) == "" {
		problems = append(problems, "private_key must not be empty")
	}

	modes := 0
	if strings.TrimSpace(ep.KnownHosts) != "" {
		modes++
	}
	if strings.TrimSpace(ep.HostKey) != "" {
		modes++
	}
	if ep.InsecureIgnoreHostKey {
		modes++
	}
	if modes > 1 {
		problems = append(problems, "known_hosts, host_key and insecure_ignore_host_key are mutually exclusive")
	}
	if ep.InsecureIgnoreHostKey && strings.TrimSpace(ep.HostKey) != "" {
		problems = append(problems, "insecure_ignore_host_key cannot be combined with host_key")
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	if !allowJump {
		return nil
	}
	if ep.KnownHosts != "" {
		if _, err := ExpandPath(ep.KnownHosts); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the complete configuration including the optional jump host.
func (c Config) Validate() error {
	if err := ValidateEndpoint(c.Endpoint, true); err != nil {
		return err
	}
	if c.Jump != nil {
		if err := ValidateEndpoint(*c.Jump, false); err != nil {
			return fmt.Errorf("jump host: %w", err)
		}
	}
	for k := range c.Envs {
		if !ValidEnvName(k) {
			return fmt.Errorf("envs contains invalid variable name %q", k)
		}
	}
	if c.Timeout < 0 || c.RetryDelay < 0 {
		return errors.New("timeout and retry_delay must not be negative")
	}
	return nil
}

// ExpandPath expands a leading ~ and returns a cleaned absolute path.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("expand %q: only ~ and ~/ paths are supported", p)
	}
	return p, nil
}

// ValidEnvName reports whether name is a portable POSIX environment variable
// name. This keeps generated command strings unambiguous.
func ValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func endpointAddress(ep Endpoint) string {
	port := ep.Port
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(ep.Host, port)
}
