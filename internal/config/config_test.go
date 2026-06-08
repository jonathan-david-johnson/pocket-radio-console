package config

import (
	"os"
	"testing"
)

// Behavior 4: save then load returns the same struct; files are mode 0600.
func TestConfigRoundTripAndPerms(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{Email: "me@example.com", Password: `p"a\ss`}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("config round-trip mismatch: got %+v want %+v", got, cfg)
	}
	assertMode0600(t, store.configPath())

	st := State{Token: "tok", UserUUID: "uuid", DeviceID: "dev"}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	gotSt, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if gotSt != st {
		t.Fatalf("state round-trip mismatch: got %+v want %+v", gotSt, st)
	}
	assertMode0600(t, store.statePath())
}

func TestLoadMissingFilesAreZero(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	if cfg, err := store.LoadConfig(); err != nil || cfg != (Config{}) {
		t.Fatalf("missing config: got %+v err %v", cfg, err)
	}
	if st, err := store.LoadState(); err != nil || st != (State{}) {
		t.Fatalf("missing state: got %+v err %v", st, err)
	}
}

func TestEnsureDeviceIDStable(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	a, err := store.EnsureDeviceID()
	if err != nil || a == "" {
		t.Fatalf("first device id: %q err %v", a, err)
	}
	b, _ := store.EnsureDeviceID()
	if a != b {
		t.Fatalf("device id not stable: %q vs %q", a, b)
	}
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("%s: mode %o, want 600", path, perm)
	}
}
