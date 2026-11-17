package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// errHostKeyMismatch marks a fixed host key rejection so IsHostKeyError can
// classify it like a knownhosts mismatch.
var errHostKeyMismatch = errors.New("host key mismatch")

// buildHostKeyCallback resolves the configured host key verification mode.
func buildHostKeyCallback(ep Endpoint) (ssh.HostKeyCallback, error) {
	if ep.InsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if ep.HostKey != "" {
		pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(ep.HostKey))
		if err != nil {
			return nil, fmt.Errorf("parse host_key: %w", err)
		}
		if len(strings.TrimSpace(string(rest))) > 0 {
			return nil, errors.New("host_key contains trailing data; expected a single authorized-key line")
		}
		inner := ssh.FixedHostKey(pub)
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if err := inner(hostname, remote, key); err != nil {
				return fmt.Errorf("fixed host key: %w", errHostKeyMismatch)
			}
			return nil
		}, nil
	}

	path := ep.KnownHosts
	if path == "" {
		def, err := DefaultKnownHostsPath()
		if err != nil {
			return nil, err
		}
		path = def
	}
	expanded, err := ExpandPath(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(expanded); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("known_hosts file %q does not exist", expanded)
		}
		return nil, fmt.Errorf("stat known_hosts file %q: %w", expanded, err)
	}
	cb, err := knownhosts.New(expanded)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts file %q: %w", expanded, err)
	}
	return cb, nil
}

func buildSigner(ep Endpoint) (ssh.Signer, error) {
	keyBytes := []byte(ep.PrivateKey)
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err == nil {
		return signer, nil
	}

	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		passphrase := ep.PrivateKeyPassphrase
		if passphrase == "" {
			passphrase = os.Getenv("SSH_PROVIDER_PRIVATE_KEY_PASSPHRASE")
		}
		if passphrase == "" {
			return nil, errors.New("private key is encrypted; provide a decrypted key or set SSH_PROVIDER_PRIVATE_KEY_PASSPHRASE")
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("parse encrypted private key: %w", err)
		}
		return signer, nil
	}
	return nil, fmt.Errorf("parse private_key: %w", err)
}
