package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// session is the persisted {node_id, token} pair, saved atomically
// (temp-file + rename) so a crash mid-write can't corrupt it — same pattern
// so credentials survive agent restarts.
type session struct {
	NodeID int64  `json:"node_id"`
	Token  string `json:"token"`
}

func loadSession(path string) (*session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveSession(path string, s *session) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
