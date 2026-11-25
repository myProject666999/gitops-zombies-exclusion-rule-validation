package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o600))

	return cfgPath
}

func TestLoadConfig(t *testing.T) {
	t.Run("missing default config yields an empty config", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

		cfg, err := loadConfig(cfgPath, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.Empty(t, cfg.ExcludeResources)
	})

	t.Run("missing explicit config returns an error", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")

		cfg, err := loadConfig(cfgPath, true)
		require.Nil(t, cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), cfgPath)
	})

	t.Run("valid config is decoded", func(t *testing.T) {
		cfgPath := writeConfig(t, `---
apiVersion: gitopszombies/v1
kind: Config
excludeResources:
- name: velero-.*
  namespace: velero
`)

		cfg, err := loadConfig(cfgPath, true)
		require.NoError(t, err)
		require.Len(t, cfg.ExcludeResources, 1)
		require.Equal(t, "velero-.*", cfg.ExcludeResources[0].Name)
		require.Equal(t, "velero", cfg.ExcludeResources[0].Namespace)
	})

	t.Run("unknown fields are rejected", func(t *testing.T) {
		cfgPath := writeConfig(t, `---
apiVersion: gitopszombies/v1
kind: Config
excludeResource:
- name: velero-.*
`)

		cfg, err := loadConfig(cfgPath, true)
		require.Nil(t, cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "excludeResource")
	})
}
