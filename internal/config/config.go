// Package config loads calibruh's JSON config file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Library struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Config struct {
	Addr string `json:"addr"`
	// Calibredb is the calibredb binary used to add books.
	Calibredb string `json:"calibredb"`
	// AllowDuplicates passes --duplicates to calibredb add.
	AllowDuplicates bool  `json:"allow_duplicates"`
	MaxUploadMB     int64 `json:"max_upload_mb"`
	// Username and Password enable HTTP Basic auth for everything, OPDS
	// included. CALIBRUH_USERNAME and CALIBRUH_PASSWORD override them so the
	// password can stay out of the file.
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	Libraries []Library `json:"libraries"`
	// LibrariesDir is watched for libraries: every subfolder holding a
	// metadata.db is served, named after the folder.
	LibrariesDir  string `json:"libraries_dir"`
	RescanSeconds int    `json:"rescan_seconds"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Addr:          ":8080",
		Calibredb:     "calibredb",
		MaxUploadMB:   500,
		RescanSeconds: 30,
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if user := os.Getenv("CALIBRUH_USERNAME"); user != "" {
		cfg.Username = user
	}
	if pw := os.Getenv("CALIBRUH_PASSWORD"); pw != "" {
		cfg.Password = pw
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if len(c.Libraries) == 0 && c.LibrariesDir == "" {
		return errors.New("config: set libraries_dir, libraries, or both")
	}
	if c.RescanSeconds <= 0 {
		return errors.New("config: rescan_seconds must be positive")
	}
	seen := map[string]bool{}
	for _, l := range c.Libraries {
		if l.Name == "" || l.Path == "" {
			return errors.New("config: every library needs a name and a path")
		}
		if seen[l.Name] {
			return fmt.Errorf("config: duplicate library name %q", l.Name)
		}
		seen[l.Name] = true
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("config: set both username and password, or neither")
	}
	if c.MaxUploadMB <= 0 {
		return errors.New("config: max_upload_mb must be positive")
	}
	return nil
}
