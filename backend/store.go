package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	db       *sql.DB
	tenantID string
	mu       sync.RWMutex
	state    State
	path     string
	aead     cipher.AEAD
}

func openStore(path string, key []byte) (*Store, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{state: newState(), path: path, aead: aead}
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.state.Providers == nil || s.state.Agents == nil || s.state.Services == nil || s.state.Runs == nil || s.state.Secrets == nil {
		return nil, fmt.Errorf("invalid persisted state")
	}
	migrateAgentCredentials(&s.state)
	for _, secret := range s.state.Secrets {
		if _, err = s.decrypt(secret); err != nil {
			return nil, fmt.Errorf("master key cannot decrypt stored secrets")
		}
	}
	for k, r := range s.state.Runs {
		if !terminal(r.Status) {
			t := now()
			r.Status = "failed"
			r.Error = "进程重启，运行中断"
			r.EndedAt = &t
			r.DurationMS = t.Sub(r.StartedAt).Milliseconds()
			for i := range r.Spans {
				if !terminal(r.Spans[i].Status) {
					r.Spans[i].Status = "failed"
					r.Spans[i].Error = r.Error
					r.Spans[i].EndedAt = &t
					r.Spans[i].DurationMS = t.Sub(r.Spans[i].StartedAt).Milliseconds()
				}
			}
			s.state.Runs[k] = r
		}
	}
	if err = s.save(s.state); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) encrypt(value string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(value), []byte(s.tenantID))), nil
}
func (s *Store) decrypt(v string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) < s.aead.NonceSize() {
		return "", fmt.Errorf("invalid secret")
	}
	plain, err := s.aead.Open(nil, b[:s.aead.NonceSize()], b[s.aead.NonceSize():], []byte(s.tenantID))
	return string(plain), err
}
func (s *Store) save(st State) error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), "state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}
func clone(st State) State {
	b, _ := json.Marshal(st)
	var out State
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) snapshot() State {
	st, e := s.snapshotE()
	if e != nil {
		panic(e)
	}
	return st
}
func (s *Store) update(fn func(*State) error) error {
	if s.db != nil {
		return s.updateSQL(fn)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return fmt.Errorf("持久化失败: %w", err)
	}
	s.state = next
	return nil
}

// Convert former provider-scoped keys into independent Agent-scoped credentials.
// Re-running migration never overwrites an already configured Agent key.
func migrateAgentCredentials(st *State) {
	for id, a := range st.Agents {
		if a.Kind == "managed" && st.Secrets[id] == "" {
			if secret := st.Secrets[a.ProviderID]; secret != "" {
				st.Secrets[id] = secret
				a.HasKey = true
				st.Agents[id] = a
			}
		}
	}
	for id, p := range st.Providers {
		delete(st.Secrets, id)
		p.Key = ""
		p.HasKey = false
		st.Providers[id] = p
	}
}
