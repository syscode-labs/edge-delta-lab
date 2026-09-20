package registry

import (
	"example.com/edge-delta-lab/hubclient"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// RepoConfig selects a repository to watch with optional tag regex filters.
type RepoConfig struct {
	Name   string `yaml:"name"`
	Allow  string `yaml:"allow,omitempty"`  // tag regex; empty = all tags eligible
	Ignore string `yaml:"ignore,omitempty"` // tag regex; wins over allow
}

// Config is the watch-registry configuration file.
type Config struct {
	HubTLS      hubclient.Config `yaml:",inline"`
	RegistryURL string           `yaml:"registry_url"`
	Poll        time.Duration    `yaml:"poll"`
	Repos       []RepoConfig     `yaml:"repos"`
	StateFile   string           `yaml:"state_file"`
	Publish     PublishConfig    `yaml:"publish"`
	Username    string           `yaml:"username,omitempty"`
	PasswordEnv string           `yaml:"password_env,omitempty"`
}

// PublishConfig points the watcher's publish trigger at the v2 pipeline.
type PublishConfig struct {
	Root          string `yaml:"root"`
	Key           string `yaml:"key"`
	Channel       string `yaml:"channel"`
	ReleasePrefix string `yaml:"release_prefix,omitempty"`
	SequenceFile  string `yaml:"sequence_file"`
}

// LoadConfig reads and validates a watcher config file.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("watch config: %w", err)
	}
	return ValidateConfig(c)
}

// ValidateConfig validates an owned config snapshot and applies defaults.
func ValidateConfig(c Config) (Config, error) {
	if c.RegistryURL == "" {
		return Config{}, fmt.Errorf("watch config: registry_url is required")
	}
	if len(c.Repos) == 0 {
		return Config{}, fmt.Errorf("watch config: at least one repo is required")
	}
	if c.Poll <= 0 {
		c.Poll = 30 * time.Second
	}
	if c.StateFile == "" {
		return Config{}, fmt.Errorf("watch config: state_file is required")
	}
	if c.Publish.Root == "" || c.Publish.Key == "" || c.Publish.Channel == "" || c.Publish.SequenceFile == "" {
		return Config{}, fmt.Errorf("watch config: publish.root, publish.key, publish.channel and publish.sequence_file are required")
	}
	for i, r := range c.Repos {
		if r.Name == "" {
			return Config{}, fmt.Errorf("watch config: repos[%d].name is required", i)
		}
		if r.Allow != "" {
			if _, err := regexp.Compile(r.Allow); err != nil {
				return Config{}, fmt.Errorf("watch config: repos[%d].allow: %w", i, err)
			}
		}
		if r.Ignore != "" {
			if _, err := regexp.Compile(r.Ignore); err != nil {
				return Config{}, fmt.Errorf("watch config: repos[%d].ignore: %w", i, err)
			}
		}
	}
	return c, nil
}

// eligible reports whether tag passes the repo's allow/ignore filters.
func (r RepoConfig) eligible(tag string) bool {
	if r.Ignore != "" {
		if re, err := regexp.Compile(r.Ignore); err == nil && re.MatchString(tag) {
			return false
		}
	}
	if r.Allow == "" {
		return true
	}
	re, err := regexp.Compile(r.Allow)
	if err != nil {
		return false
	}
	return re.MatchString(tag)
}
