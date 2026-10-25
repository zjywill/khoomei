// Package config reads and writes <home>/config.json (SPEC.md §6).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/zjywill/khoomei/internal/protocol"
)

const DefaultPort = 7767

// Roles and caller transports.
const (
	RoleHub    = "hub"
	RoleCaller = "caller"

	TransportDirect = "direct" // the host already routes to the tailnet (Muse's built-in connector)
	TransportTSNet  = "tsnet"  // dial through an embedded tailnet node of our own
)

type WakeAdapter struct {
	Type       string   `json:"type"` // "webhook" | "exec"
	URLFile    string   `json:"url_file,omitempty"`
	SecretFile string   `json:"secret_file,omitempty"`
	Argv       []string `json:"argv,omitempty"`
}

type AgentSpec struct {
	Node     string       `json:"node,omitempty"`  // tailnet node the agent connects from (short name or FQDN)
	Local    bool         `json:"local,omitempty"` // lives on the hub machine
	Wake     *WakeAdapter `json:"wake,omitempty"`
	WakeMax  string       `json:"wake_max,omitempty"`
	WakeRate int          `json:"wake_rate,omitempty"`
}

// OwnerSpec binds an owner handle to a Tailscale login (alice@github, bob@example.com).
type OwnerSpec struct {
	Login string `json:"login"`
}

// Retention is how long the hub keeps acknowledged records (docs/PANEL.md#清理). Pending records are never
// pruned automatically, and nothing younger than MinPruneAge is pruned at all.
type Retention struct {
	AckedDays *int `json:"acked_days,omitempty"` // nil: DefaultAckedDays; 0: keep forever
	MaxMB     int  `json:"max_mb,omitempty"`     // cap on acked/ in MiB; 0: no cap
}

const DefaultAckedDays = 3

// AckedDaysOrDefault resolves the configured retention; a negative value counts as 0.
func (r Retention) AckedDaysOrDefault() int {
	if r.AckedDays == nil {
		return DefaultAckedDays
	}
	return max(*r.AckedDays, 0)
}

// Blob limits the files agents attach to messages (docs/FILES.md).
type Blob struct {
	MaxMB     int `json:"max_mb,omitempty"`      // largest single file; 0: DefaultBlobMaxMB
	MinFreeMB int `json:"min_free_mb,omitempty"` // refuse uploads below this much free disk; 0: DefaultBlobMinFreeMB
}

const (
	DefaultBlobMaxMB     = 25
	DefaultBlobMinFreeMB = 500
)

func (b Blob) MaxBytes() int64 {
	if b.MaxMB <= 0 {
		return DefaultBlobMaxMB << 20
	}
	return int64(b.MaxMB) << 20
}

func (b Blob) MinFreeBytes() uint64 {
	if b.MinFreeMB <= 0 {
		return DefaultBlobMinFreeMB << 20
	}
	return uint64(b.MinFreeMB) << 20
}

type TSNet struct {
	Dir         string `json:"dir,omitempty"`      // state directory; default <home>/tsnet
	Hostname    string `json:"hostname,omitempty"` // node name on the tailnet; default khoomei-<owner>[-<agent>]
	AuthKeyFile string `json:"auth_key_file,omitempty"`
}

type Policy struct {
	Mode string   `json:"mode"`
	Deny []string `json:"deny"` // node names, short names or logins
}

type Config struct {
	Role      string                `json:"role"`
	Owner     string                `json:"owner,omitempty"` // this machine's owner handle; bare names qualify to it
	Agent     string                `json:"agent,omitempty"`
	Hub       string                `json:"hub,omitempty"`       // callers: HOST[:PORT] of the hub
	Transport string                `json:"transport,omitempty"` // callers: direct | tsnet
	Port      int                   `json:"port,omitempty"`
	TSNet     TSNet                 `json:"tsnet"`
	Policy    Policy                `json:"policy"`
	Owners    map[string]*OwnerSpec `json:"owners,omitempty"` // hub: other people whose agents may connect
	Agents    map[string]*AgentSpec `json:"agents"`           // hub: keyed by full address once Owner is set
	Retention Retention             `json:"retention"`        // hub: automatic pruning of acked records
	Blob      Blob                  `json:"blob"`             // hub: attachment limits

	Home string `json:"-"`
	Path string `json:"-"`
}

// Load reads the config under home, returning defaults when none exists.
func Load(home string) (*Config, error) {
	c := &Config{Role: RoleCaller, Transport: TransportDirect, Port: DefaultPort,
		Policy: Policy{Mode: "tailnet", Deny: []string{}}, Agents: map[string]*AgentSpec{},
		Home: home, Path: filepath.Join(home, "config.json")}
	b, err := os.ReadFile(c.Path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Path, err)
	}
	if c.Agents == nil {
		c.Agents = map[string]*AgentSpec{}
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Transport == "" {
		c.Transport = TransportDirect
	}
	c.normalize()
	return c, nil
}

// normalize rewrites bare agent keys to owner-qualified ones once an owner handle exists.
func (c *Config) normalize() {
	if c.Owner == "" {
		return
	}
	for k, v := range c.Agents {
		if q := protocol.Qualify(k, c.Owner); q != k {
			delete(c.Agents, k)
			c.Agents[q] = v
		}
	}
}

// Exists reports whether a config file is present.
func (c *Config) Exists() bool {
	_, err := os.Stat(c.Path)
	return err == nil
}

// Save writes the config, keeping the previous version as config.json.bak.
func (c *Config) Save() error {
	c.normalize()
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(c.Path); err == nil {
		_ = os.WriteFile(c.Path+".bak", old, 0o600)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// Pretty returns the JSON form for printing plans.
func (c *Config) Pretty() string {
	b, _ := json.MarshalIndent(c, "", "  ")
	return string(b)
}

// HubAddr returns the hub's host:port for callers.
func (c *Config) HubAddr() (string, error) {
	if c.Hub == "" {
		return "", errors.New("no hub configured; run: khoomei setup --hub HOST[:PORT] --agent NAME --apply")
	}
	if _, _, err := net.SplitHostPort(c.Hub); err != nil {
		return net.JoinHostPort(c.Hub, strconv.Itoa(DefaultPort)), nil
	}
	return c.Hub, nil
}

// SelfAddress is this machine's own agent address: owner/agent when an owner handle is set.
func (c *Config) SelfAddress() string {
	return protocol.Qualify(c.Agent, c.Owner)
}

// TSNetDir is the embedded node's state directory.
func (c *Config) TSNetDir() string {
	if c.TSNet.Dir != "" {
		return c.TSNet.Dir
	}
	return filepath.Join(c.Home, "tsnet")
}

// TSNetHostname is the embedded node's name on the tailnet: khoomei-<owner> for a hub,
// khoomei-<owner>-<agent> for a caller; "hub" stands in for a missing owner.
func (c *Config) TSNetHostname() string {
	if c.TSNet.Hostname != "" {
		return c.TSNet.Hostname
	}
	owner := c.Owner
	if owner == "" {
		owner = "hub"
	}
	if c.Role == RoleHub {
		return "khoomei-" + owner
	}
	return "khoomei-" + owner + "-" + c.Agent
}

// Spec returns the agent's spec by full address, creating an empty one if absent.
func (c *Config) Spec(addr string) *AgentSpec {
	addr = protocol.Qualify(addr, c.Owner)
	if c.Agents[addr] == nil {
		c.Agents[addr] = &AgentSpec{}
	}
	return c.Agents[addr]
}

// Lookup finds an agent by bare or qualified address.
func (c *Config) Lookup(addr string) (*AgentSpec, bool) {
	s, ok := c.Agents[protocol.Qualify(addr, c.Owner)]
	return s, ok
}
