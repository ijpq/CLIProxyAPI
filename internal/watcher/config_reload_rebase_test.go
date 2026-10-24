package watcher

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigTruncatedBetweenCheckAndLoadPreservesActiveSettings(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}
	active, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if errWrite := writeIssue6631Config(configPath, authDir, 2); errWrite != nil {
		t.Fatal(errWrite)
	}
	originalLoad := loadConfigFileFunc
	t.Cleanup(func() { loadConfigFileFunc = originalLoad })
	truncate := true
	loadConfigFileFunc = func(path string) (*config.Config, error) {
		if truncate {
			truncate = false
			if errWrite := os.WriteFile(path, nil, 0o600); errWrite != nil {
				return nil, errWrite
			}
		}
		return originalLoad(path)
	}
	var applied []int
	w := &Watcher{
		configPath: configPath, authDir: authDir, config: active, lastConfigHash: active.SourceHash,
		reloadCallback: func(cfg *config.Config) { applied = append(applied, cfg.RequestRetry) },
	}
	w.ReloadConfigIfChanged()
	if len(applied) != 0 || w.config != active || w.lastConfigHash != active.SourceHash {
		t.Fatal("transient empty read replaced active config or acknowledged an unapplied write")
	}
	if errWrite := writeIssue6631Config(configPath, authDir, 3); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.ReloadConfigIfChanged()
	if len(applied) != 1 || applied[0] != 3 || w.config.AuthDir != authDir {
		t.Fatalf("complete config was not applied after transient truncation: retries=%v auth-dir=%q", applied, w.config.AuthDir)
	}
}
