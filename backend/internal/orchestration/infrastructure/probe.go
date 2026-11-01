// Package orchestrationinfra contains the non-PostgreSQL adapters of the
// orchestration context. The shared-storage readiness probe lives here; the
// api_instance repository and the advisory-lock leader live in
// infrastructure/postgres.
package orchestrationinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
)

// SettingDataRoot is the shared content root every API replica must see.
const SettingDataRoot = "paths.data_root"

// RootReader is the storage probe's view of the settings centre.
type RootReader interface {
	String(ctx context.Context, key string) (string, error)
}

// StorageProbe verifies that paths.data_root is writable from this replica. It
// writes data_root/.probe-<sha256(instance_key)[:16]> and removes it again, so
// a replica mounted on a different (or read-only) filesystem fails readiness
// instead of silently splitting content. The fixed per-instance name avoids
// accumulating files and keeps the check attributable in logs.
type StorageProbe struct {
	settings    RootReader
	instanceKey string
}

// NewStorageProbe wires the shared-storage probe.
func NewStorageProbe(settings RootReader, instanceKey string) *StorageProbe {
	return &StorageProbe{settings: settings, instanceKey: instanceKey}
}

// Probe performs the write-then-delete check.
func (p *StorageProbe) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.settings == nil {
		return fmt.Errorf("orchestration storage probe is not configured")
	}
	instanceKey := strings.TrimSpace(p.instanceKey)
	if instanceKey == "" {
		return fmt.Errorf("orchestration storage probe requires an instance key")
	}
	root, err := p.settings.String(ctx, SettingDataRoot)
	if err != nil {
		return fmt.Errorf("read %s: %w", SettingDataRoot, err)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("%s is empty", SettingDataRoot)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return fmt.Errorf("create data root: %w", err)
	}

	sum := sha256.Sum256([]byte(instanceKey))
	probePath := filepath.Join(absoluteRoot, ".probe-"+hex.EncodeToString(sum[:8]))
	file, err := os.OpenFile(probePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create storage probe: %w", err)
	}
	if _, err := file.Write([]byte("eztest storage probe")); err != nil {
		_ = file.Close()
		_ = os.Remove(probePath)
		return fmt.Errorf("write storage probe: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("close storage probe: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("remove storage probe: %w", err)
	}
	return nil
}

var _ orchestrationapp.StorageProbe = (*StorageProbe)(nil)
