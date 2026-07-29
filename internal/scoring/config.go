// Package scoring turns recorded signals into a ranked prospect score with the
// reasoning attached.
//
// The reasoning is the product. A score of 85 with no explanation cannot open a
// cold email, so every component carries the evidence that produced it and the
// engine assembles a sentence a human can send.
package scoring

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/EOEboh/prospects-cli/internal/model"
)

//go:embed weights.default.yaml
var defaultWeightsYAML []byte

// Config is a scoring configuration: the rules, their weights, and the
// thresholds that interpret them.
type Config struct {
	Version       int     `yaml:"version"`
	MinConfidence float64 `yaml:"min_confidence"`

	SizeBands SizeBands `yaml:"size_bands"`
	Rules     []Rule    `yaml:"rules"`

	// ManualCheckSignal names the signal whose absence means a prospect has
	// not been fully assessed. Empty disables the flag.
	ManualCheckSignal string `yaml:"manual_check_signal"`

	// hash identifies which configuration produced a score, so scores from
	// before and after a retune stay distinguishable.
	hash string
}

// SizeBands maps a review count onto an employee-size band. Rough by design:
// it separates a two-person shop from a national chain, nothing finer.
type SizeBands struct {
	MicroBelow int `yaml:"micro_below"`
	SMBBelow   int `yaml:"smb_below"`
	MidBelow   int `yaml:"mid_below"`
}

// Rule is one scoring component.
type Rule struct {
	ID     string    `yaml:"id"`
	Label  string    `yaml:"label"`
	Points int       `yaml:"points"`
	When   Condition `yaml:"when"`

	// Explain is a third-person clause used in the score breakdown:
	// "has a contact form with no CRM behind it".
	Explain string `yaml:"explain"`

	// Observation is an optional second-person opener for `brief`, phrased to
	// be sent. Only rules that make a good opening line need one.
	Observation string `yaml:"observation"`
}

// Condition is a small declarative test over a business's current signals.
//
// It is deliberately not a general expression language: the rules that matter
// here are "this signal equals that", "this number is at least that", and
// combinations of those. Anything more would be a scripting engine to debug.
type Condition struct {
	// Signal is the signal type to inspect.
	Signal string `yaml:"signal"`

	Equals  string   `yaml:"equals"`
	OneOf   []string `yaml:"one_of"`
	AtLeast *int     `yaml:"at_least"`
	AtMost  *int     `yaml:"at_most"`

	// Present matches when any current signal of this type exists, whatever
	// its value. Absent matches when none does.
	Present bool `yaml:"present"`
	Absent  bool `yaml:"absent"`

	All []Condition `yaml:"all"`
	Any []Condition `yaml:"any"`
	Not *Condition  `yaml:"not"`
}

// Load reads a weights file, falling back to the built-in defaults when path
// is empty or the file does not exist. A missing weights.yaml is the normal
// case, not an error: `prospect score` works out of the box.
func Load(path string) (*Config, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			cfg, err := Parse(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}
	return Parse(defaultWeightsYAML)
}

// DefaultYAML returns the built-in configuration, for `--print-config` and for
// writing a starting weights.yaml.
func DefaultYAML() []byte { return defaultWeightsYAML }

// Parse reads and validates a configuration.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse weights: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(data)
	cfg.hash = hex.EncodeToString(sum[:])[:12]
	return &cfg, nil
}

// Hash identifies the configuration that produced a score.
func (c *Config) Hash() string { return c.hash }

func (c *Config) validate() error {
	if len(c.Rules) == 0 {
		return fmt.Errorf("weights define no rules")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		return fmt.Errorf("min_confidence must be between 0 and 1, got %v", c.MinConfidence)
	}

	seen := make(map[string]bool, len(c.Rules))
	for i, r := range c.Rules {
		switch {
		case r.ID == "":
			return fmt.Errorf("rule %d has no id", i)
		case seen[r.ID]:
			return fmt.Errorf("duplicate rule id %q", r.ID)
		case r.Points == 0:
			return fmt.Errorf("rule %q scores zero points; remove it instead", r.ID)
		case r.Explain == "":
			return fmt.Errorf("rule %q has no explain text; a score without reasoning is useless", r.ID)
		}
		seen[r.ID] = true

		if err := r.When.validate(r.ID); err != nil {
			return err
		}
	}

	if c.ManualCheckSignal != "" {
		if _, err := model.LookupSignalType(c.ManualCheckSignal); err != nil {
			return fmt.Errorf("manual_check_signal: %w", err)
		}
	}
	if c.SizeBands.MicroBelow > c.SizeBands.SMBBelow || c.SizeBands.SMBBelow > c.SizeBands.MidBelow {
		return fmt.Errorf("size_bands thresholds must increase: micro_below <= smb_below <= mid_below")
	}
	return nil
}

func (c Condition) validate(ruleID string) error {
	nested := len(c.All) + len(c.Any)
	if c.Not != nil {
		nested++
	}

	if c.Signal == "" && nested == 0 {
		return fmt.Errorf("rule %q has an empty condition", ruleID)
	}

	if c.Signal != "" {
		// The vocabulary is closed, so a typo in weights.yaml fails at load
		// rather than silently never matching.
		if _, err := model.LookupSignalType(c.Signal); err != nil {
			return fmt.Errorf("rule %q: %w", ruleID, err)
		}
		tests := 0
		for _, set := range []bool{c.Equals != "", len(c.OneOf) > 0, c.AtLeast != nil, c.AtMost != nil, c.Present, c.Absent} {
			if set {
				tests++
			}
		}
		if tests == 0 {
			return fmt.Errorf("rule %q: condition on %q tests nothing", ruleID, c.Signal)
		}
		if c.Present && c.Absent {
			return fmt.Errorf("rule %q: condition cannot require %q to be both present and absent", ruleID, c.Signal)
		}
	}

	for _, sub := range c.All {
		if err := sub.validate(ruleID); err != nil {
			return err
		}
	}
	for _, sub := range c.Any {
		if err := sub.validate(ruleID); err != nil {
			return err
		}
	}
	if c.Not != nil {
		return c.Not.validate(ruleID)
	}
	return nil
}

// MaxPossible is the sum of positive points, the denominator every score is
// normalized against.
func (c *Config) MaxPossible() int {
	total := 0
	for _, r := range c.Rules {
		if r.Points > 0 {
			total += r.Points
		}
	}
	return total
}

// BandFor maps a review count onto a size band.
func (c *Config) BandFor(reviews int) string {
	switch {
	case reviews < c.SizeBands.MicroBelow:
		return model.SizeMicro
	case reviews < c.SizeBands.SMBBelow:
		return model.SizeSMB
	case reviews < c.SizeBands.MidBelow:
		return model.SizeMid
	default:
		return model.SizeLarge
	}
}

// signalTypes lists every signal type a rule's condition inspects, which is
// how coverage decides whether the rule could be checked at all.
func (r Rule) signalTypes() []model.SignalType {
	var out []model.SignalType
	var walk func(Condition)
	walk = func(c Condition) {
		if c.Signal != "" {
			out = append(out, model.SignalType(c.Signal))
		}
		for _, sub := range c.All {
			walk(sub)
		}
		for _, sub := range c.Any {
			walk(sub)
		}
		if c.Not != nil {
			walk(*c.Not)
		}
	}
	walk(r.When)
	return out
}

// DescribeSource renders where the configuration came from, for log lines and
// the run record. Knowing a score came from the defaults rather than a tuned
// file explains a lot when the numbers look wrong.
func DescribeSource(path string) string {
	if strings.TrimSpace(path) == "" {
		return "built-in defaults"
	}
	if _, err := os.Stat(path); err != nil {
		return "built-in defaults (" + path + " not found)"
	}
	return path
}
