package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultHTTPAddr = "127.0.0.1:18500"
	defaultProxyURL = ""
)

var buildFlavor = ""

type App struct {
	RootDir       string
	DataDir       string
	ProfilesDir   string
	DatabasePath  string
	WebDistDir    string
	MigrationsDir string
	HTTPAddr      string
	ProxyURL      string
	NoWindow      bool
	IsWindows     bool
}

func Load() (App, error) {
	exeDir, err := executableDir()
	if err != nil {
		return App{}, err
	}

	root := exeDir
	if buildFlavor != "release" {
		root, err = findRoot(exeDir)
		if err != nil {
			return App{}, err
		}
	}

	cfg := App{
		RootDir:       root,
		WebDistDir:    filepath.Join(root, "web-dist"),
		MigrationsDir: filepath.Join(root, "migrations"),
		HTTPAddr:      envOrDefault("APP_ADDR", defaultHTTPAddr),
		ProxyURL:      envOrDefault("APP_PROXY_URL", defaultProxyURL),
		NoWindow:      strings.EqualFold(os.Getenv("APP_NO_WINDOW"), "1") || strings.EqualFold(os.Getenv("APP_NO_WINDOW"), "true"),
		IsWindows:     runtime.GOOS == "windows",
	}

	if hasProjectLayout(root) {
		cfg.DataDir = filepath.Join(root, "data")
		cfg.ProfilesDir = filepath.Join(root, "data", "profiles")
		cfg.DatabasePath = filepath.Join(root, "data", "youtubeshort.db")
	} else {
		cfg.DataDir = filepath.Join(root, "data")
		cfg.ProfilesDir = filepath.Join(root, "data", "profiles")
		cfg.DatabasePath = filepath.Join(root, "youtubeshort.db")
	}

	for _, dir := range []string{cfg.DataDir, cfg.ProfilesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return App{}, err
		}
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func findRoot(exeDir string) (string, error) {
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if exeDir != "" {
		candidates = append(candidates, exeDir)
	}

	for _, base := range candidates {
		current := base
		for range 6 {
			if current == "" {
				break
			}
			if fileExists(filepath.Join(current, ".prompt")) || fileExists(filepath.Join(current, "web-dist")) {
				return current, nil
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	if exeDir != "" {
		return exeDir, nil
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return "", errors.New("project root not found")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasProjectLayout(root string) bool {
	for _, name := range []string{".prompt", "cmd", "internal", "web-dist"} {
		if fileExists(filepath.Join(root, name)) {
			return true
		}
	}
	return false
}
