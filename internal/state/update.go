package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type UpdateStatus struct {
	CheckedAt       time.Time `json:"checked_at"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	NotifiedAt      time.Time `json:"notified_at,omitempty"`
	NotifiedVersion string    `json:"notified_version,omitempty"`
}

func (s *Store) UpdateStatusPath() string { return filepath.Join(s.Dir, "update-status.json") }

func (s *Store) LoadUpdateStatus() (UpdateStatus, error) {
	var status UpdateStatus
	body, err := os.ReadFile(s.UpdateStatusPath())
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("读取更新状态: %w", err)
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return status, fmt.Errorf("解析更新状态: %w", err)
	}
	return status, nil
}

func (s *Store) SaveUpdateStatus(status UpdateStatus) error {
	status.LatestVersion = strings.TrimSpace(status.LatestVersion)
	status.NotifiedVersion = strings.TrimSpace(status.NotifiedVersion)
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(s.UpdateStatusPath(), append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(s.UpdateStatusPath(), 0o600)
}
