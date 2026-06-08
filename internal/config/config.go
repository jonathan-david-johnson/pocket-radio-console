// Package config loads and saves config.toml (creds) and state.json (token,
// device id) under the XDG config dir at mode 0600. No OS keychain — see
// docs/console/adr/0003-token-in-config-file.md.
package config

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds account credentials from config.toml.
type Config struct {
	Email    string `toml:"email"`
	Password string `toml:"password"`
}

// State holds the cached token and device identity from state.json.
type State struct {
	Token    string `json:"token"`
	UserUUID string `json:"user_uuid"`
	DeviceID string `json:"device_id"`
}

// Store reads/writes config + state under a directory (the XDG config dir in
// production; a temp dir in tests).
type Store struct {
	Dir string
}

// DefaultDir returns $XDG_CONFIG_HOME/pocket-radio or ~/.config/pocket-radio.
func DefaultDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pocket-radio"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "pocket-radio"), nil
}

// NewStore returns a Store for dir, creating it at mode 0700 if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) configPath() string { return filepath.Join(s.Dir, "config.toml") }
func (s *Store) statePath() string  { return filepath.Join(s.Dir, "state.json") }

// LoadConfig reads config.toml. A missing file yields a zero Config, no error.
func (s *Store) LoadConfig() (Config, error) {
	b, err := os.ReadFile(s.configPath())
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	return parseTOML(string(b)), nil
}

// SaveConfig writes config.toml at mode 0600.
func (s *Store) SaveConfig(c Config) error {
	content := fmt.Sprintf("email = %q\npassword = %q\n", c.Email, c.Password)
	return writeFile0600(s.configPath(), []byte(content))
}

// LoadState reads state.json. A missing file yields a zero State, no error.
func (s *Store) LoadState() (State, error) {
	b, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, err
	}
	return st, nil
}

// SaveState writes state.json at mode 0600.
func (s *Store) SaveState(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile0600(s.statePath(), b)
}

// EnsureDeviceID returns the persisted device id, creating + saving one if absent.
func (s *Store) EnsureDeviceID() (string, error) {
	st, err := s.LoadState()
	if err != nil {
		return "", err
	}
	if st.DeviceID != "" {
		return st.DeviceID, nil
	}
	st.DeviceID = newUUID()
	if err := s.SaveState(st); err != nil {
		return "", err
	}
	return st.DeviceID, nil
}

// newUUID returns a random RFC-4122 v4 UUID string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writeFile0600 writes atomically-ish at mode 0600, forcing the perm even if the
// file already existed with looser bits.
func writeFile0600(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// parseTOML handles the two flat string keys we persist. It deliberately does
// not pull in a full TOML parser for this tiny surface.
func parseTOML(s string) Config {
	var c Config
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = unquote(strings.TrimSpace(val))
		switch key {
		case "email":
			c.Email = val
		case "password":
			c.Password = val
		}
	}
	return c
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"') {
		inner := s[1 : len(s)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return s
}

// PromptCredentials asks for email + password on the terminal (in is os.Stdin).
func PromptCredentials(in *os.File) (Config, error) {
	r := bufio.NewReader(in)
	fmt.Fprint(os.Stderr, "Pocket Casts email: ")
	email, err := r.ReadString('\n')
	if err != nil {
		return Config{}, err
	}
	fmt.Fprint(os.Stderr, "Pocket Casts password: ")
	password, err := r.ReadString('\n')
	if err != nil {
		return Config{}, err
	}
	return Config{
		Email:    strings.TrimSpace(email),
		Password: strings.TrimSpace(password),
	}, nil
}
