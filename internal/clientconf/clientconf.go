// Package clientconf loads the v3 client YAML configuration: which origin to
// follow, which pinned key, where state and cache live, which releases are
// eligible (allow/ignore regexes), and what action may be taken after a fully
// verified sync (dry-run | load | restart).
//
// Gating is decision-only: applied BEFORE any download, so an ineligible
// release never costs bytes. Action execution reuses the v2 paths — `load`
// is lab.Sync with DockerLoad enabled (signature, chunk, size, whole-archive
// checks all happen before any Docker call), and `restart` only ever
// recreates containers whose image ID matches the manifest's signed image IDs.
package clientconf

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Action is the explicit post-verification mode. Default is DryRun.
type Action string

const (
	ActionDryRun  Action = "dry-run"
	ActionLoad    Action = "load"
	ActionRestart Action = "restart"
)

// NotifierConfig mirrors notify.NotifierConfig for client YAML readability.
type NotifierConfig struct {
	Type       string `yaml:"type"`
	TokenEnv   string `yaml:"token_env,omitempty"`
	ChatID     string `yaml:"chat_id,omitempty"`
	WebhookEnv string `yaml:"webhook_env,omitempty"`
}

// Config is the client configuration file (YAML).
type Config struct {
	Origin   string `yaml:"origin"`   // origin base URL (http allowed only with allow_http)
	Manifest string `yaml:"manifest"` // signed manifest/channel URL
	PubKey   string `yaml:"pub_key"`  // pinned publisher public key
	StateDir string `yaml:"state_dir"`
	CacheDir string `yaml:"cache_dir,omitempty"` // optional alias for state_dir chunks

	DeviceID   string `yaml:"device_id,omitempty"`
	AllowHTTP  bool   `yaml:"allow_http,omitempty"`
	Poll       string `yaml:"poll,omitempty"`        // watch interval (time.Duration syntax)
	EventsURL  string `yaml:"events_url,omitempty"`  // hub ws:// announce endpoint (hints only)
	Workers    int    `yaml:"workers,omitempty"`
	Action     Action `yaml:"action,omitempty"` // dry-run (default) | load | restart
	Allow      string `yaml:"allow,omitempty"`  // release-name regex; empty = all
	Ignore     string `yaml:"ignore,omitempty"` // release-name regex; wins over allow
	MaxReleases int   `yaml:"max_releases,omitempty"` // safety bound for dry-run enumeration

	Notifiers []NotifierConfig `yaml:"notifiers,omitempty"`

	// Restart is only consulted when Action == restart.
	Restart RestartConfig `yaml:"restart,omitempty"`
}

// RestartConfig controls container recreation after a verified load.
type RestartConfig struct {
	// DockerCLI overrides the docker binary path (tests inject a fake CLI;
	// default "docker" resolved from PATH at exec time).
	DockerCLI string `yaml:"docker_cli,omitempty"`
	// Include/Exclude container-name regexes, applied to the image-matched
	// container list. Include empty = all matched; Exclude wins.
	Include string `yaml:"include,omitempty"`
	Exclude string `yaml:"exclude,omitempty"`
	// StopTimeout seconds passed to docker stop (default 10).
	StopTimeout int `yaml:"stop_timeout,omitempty"`
}

// Load reads and validates a client config file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

// Parse validates config bytes; defaults are applied after validation.
func Parse(b []byte) (Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("client config: %w", err)
	}
	if c.Origin == "" {
		return Config{}, fmt.Errorf("client config: origin is required")
	}
	if c.Manifest == "" {
		return Config{}, fmt.Errorf("client config: manifest is required")
	}
	if c.PubKey == "" {
		return Config{}, fmt.Errorf("client config: pub_key is required")
	}
	if c.StateDir == "" {
		return Config{}, fmt.Errorf("client config: state_dir is required")
	}
	switch c.Action {
	case "":
		c.Action = ActionDryRun
	case ActionDryRun, ActionLoad, ActionRestart:
	default:
		return Config{}, fmt.Errorf("client config: action must be dry-run, load or restart (got %q)", c.Action)
	}
	for _, field := range []struct{ name, re string }{{"allow", c.Allow}, {"ignore", c.Ignore}, {"restart.include", c.Restart.Include}, {"restart.exclude", c.Restart.Exclude}} {
		if field.re != "" {
			if _, err := regexp.Compile(field.re); err != nil {
				return Config{}, fmt.Errorf("client config: %s: %w", field.name, err)
			}
		}
	}
	if c.Restart.DockerCLI == "" {
		c.Restart.DockerCLI = "docker"
	}
	if c.Restart.StopTimeout == 0 {
		c.Restart.StopTimeout = 10
	}
	if c.Workers < 0 || c.MaxReleases < 0 {
		return Config{}, fmt.Errorf("client config: workers and max_releases must be >= 0")
	}
	return c, nil
}

// Eligible applies allow/ignore gating to a release name. Ignore wins over
// allow; an empty allow matches everything. Callers must gate BEFORE download.
func (c Config) Eligible(release string) bool {
	if c.Ignore != "" {
		if re, err := regexp.Compile(c.Ignore); err == nil && re.MatchString(release) {
			return false
		}
	}
	if c.Allow == "" {
		return true
	}
	re, err := regexp.Compile(c.Allow)
	if err != nil {
		return false
	}
	return re.MatchString(release)
}

// Plan is what a dry-run (or a gated action run) reports for one release.
type Plan struct {
	Release   string   `json:"release"`
	Eligible  bool     `json:"eligible"`
	Action    Action   `json:"action"`
	WouldDo   []string `json:"would_do,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

// SortPlans orders plans by release name for stable output.
func SortPlans(plans []Plan) {
	sort.Slice(plans, func(i, j int) bool { return plans[i].Release < plans[j].Release })
}

// ValidActions is the canonical action list, for usage text.
var ValidActions = []string{string(ActionDryRun), string(ActionLoad), string(ActionRestart)}
