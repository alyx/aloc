// Package config loads persistent defaults from a YAML config file.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/alyx/aloc/internal/detect"
	"github.com/alyx/aloc/internal/lang"
)

// Config mirrors the .aloc.yml schema. Pointer fields distinguish "not set"
// from an explicit false/zero so CLI flags and file values merge cleanly.
type Config struct {
	Format         *string  `yaml:"format"`
	SmartExclude   *bool    `yaml:"smart_exclude"`
	Gitignore      *bool    `yaml:"gitignore"`
	Hidden         *bool    `yaml:"hidden"`
	FollowSymlinks *bool    `yaml:"follow_symlinks"`
	ByFile         *bool    `yaml:"by_file"`
	Jobs           *int     `yaml:"jobs"`
	Exclude        []string `yaml:"exclude"`
	Include        []string `yaml:"include"`
	Extensions     []string `yaml:"extensions"`
	Languages      []string `yaml:"languages"`

	Detectors struct {
		Disable []string         `yaml:"disable"`
		Custom  []CustomDetector `yaml:"custom"`
	} `yaml:"detectors"`

	Definitions map[string]Definition `yaml:"definitions"`

	// Path the config was loaded from, for error messages. Not serialized.
	Path string `yaml:"-"`
}

// CustomDetector is a user-defined smart-exclusion rule.
type CustomDetector struct {
	Name        string   `yaml:"name"`
	Markers     []string `yaml:"markers"`
	Exclude     []string `yaml:"exclude"`
	SelfMarkers []string `yaml:"self_markers"`
	SelfName    string   `yaml:"self_name"`
}

// Definition adds or overrides a language definition.
type Definition struct {
	Extensions    []string    `yaml:"extensions"`
	Filenames     []string    `yaml:"filenames"`
	Shebangs      []string    `yaml:"shebangs"`
	LineComments  []string    `yaml:"line_comments"`
	BlockComments [][2]string `yaml:"block_comments"`
	Quotes        [][2]string `yaml:"quotes"`
	MultiQuotes   [][2]string `yaml:"multiline_quotes"`
	RawQuotes     [][2]string `yaml:"raw_quotes"`
	Nested        bool        `yaml:"nested_comments"`
}

// Load reads the config file. explicit is the --config value; when empty the
// search order is ./.aloc.yml, ./.aloc.yaml, then
// $XDG_CONFIG_HOME/aloc/config.yml (or ~/.config/aloc/config.yml). A missing
// file is not an error — an empty Config is returned.
func Load(explicit string) (*Config, error) {
	var candidates []string
	if explicit != "" {
		candidates = []string{explicit}
	} else {
		candidates = []string{".aloc.yml", ".aloc.yaml"}
		if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
			candidates = append(candidates, filepath.Join(base, "aloc", "config.yml"))
		} else if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".config", "aloc", "config.yml"))
		}
	}

	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		var c Config
		dec := yaml.NewDecoder(bytes.NewReader(content))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil && err != io.EOF {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
		c.Path = path
		return &c, nil
	}
	if explicit != "" {
		return nil, fmt.Errorf("config file %s not found", explicit)
	}
	return &Config{}, nil
}

// CustomDetectors converts the config's detectors into engine form.
func (c *Config) CustomDetectors() []detect.Detector {
	out := make([]detect.Detector, 0, len(c.Detectors.Custom))
	for _, d := range c.Detectors.Custom {
		out = append(out, detect.Detector{
			Name:        d.Name,
			Markers:     d.Markers,
			ExcludeDirs: d.Exclude,
			SelfMarkers: d.SelfMarkers,
			SelfName:    d.SelfName,
		})
	}
	return out
}

// ApplyDefinitions merges user language definitions into the registry.
func (c *Config) ApplyDefinitions(r *lang.Registry) error {
	for name, d := range c.Definitions {
		l := &lang.Language{
			Name:          name,
			Extensions:    d.Extensions,
			Filenames:     d.Filenames,
			Shebangs:      d.Shebangs,
			LineComments:  d.LineComments,
			BlockComments: d.BlockComments,
			Quotes:        d.Quotes,
			MultiQuotes:   d.MultiQuotes,
			RawQuotes:     d.RawQuotes,
			Nested:        d.Nested,
		}
		if err := l.Validate(); err != nil {
			return fmt.Errorf("config %s: %w", c.Path, err)
		}
		r.Add(l)
	}
	return nil
}
