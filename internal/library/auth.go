package library

import (
	"context"
	"errors"

	"pocket-radio-console/internal/config"
	"pocket-radio-console/internal/pocketcasts"
)

// StoreAuth is the production Auth: it reads the cached token from a config.Store
// and re-logs-in from config.toml creds (caching the new token + uuid).
type StoreAuth struct {
	Store    *config.Store
	API      pocketcasts.PocketCasts
	Config   config.Config
	deviceID string
}

// NewStoreAuth wires an Auth from a store, api, and loaded creds.
func NewStoreAuth(store *config.Store, api pocketcasts.PocketCasts, cfg config.Config) (*StoreAuth, error) {
	dev, err := store.EnsureDeviceID()
	if err != nil {
		return nil, err
	}
	return &StoreAuth{Store: store, API: api, Config: cfg, deviceID: dev}, nil
}

// Token returns the cached token from state.json, if any.
func (a *StoreAuth) Token() (string, bool) {
	st, err := a.Store.LoadState()
	if err != nil || st.Token == "" {
		return "", false
	}
	return st.Token, true
}

// DeviceID returns the stable device id.
func (a *StoreAuth) DeviceID() string { return a.deviceID }

// UserUUID returns the cached Pocket Casts user UUID (for Supabase favorites).
func (a *StoreAuth) UserUUID() string {
	st, err := a.Store.LoadState()
	if err != nil {
		return ""
	}
	return st.UserUUID
}

// Relogin authenticates from config creds and caches token + uuid in state.json.
func (a *StoreAuth) Relogin(ctx context.Context) (string, error) {
	if a.Config.Email == "" || a.Config.Password == "" {
		return "", errors.New("no credentials configured")
	}
	sess, err := a.API.Login(ctx, a.Config.Email, a.Config.Password)
	if err != nil {
		return "", err
	}
	st, _ := a.Store.LoadState()
	st.Token = sess.Token
	st.UserUUID = sess.UUID
	if st.DeviceID == "" {
		st.DeviceID = a.deviceID
	}
	if err := a.Store.SaveState(st); err != nil {
		return "", err
	}
	return sess.Token, nil
}
