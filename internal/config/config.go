package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EOEboh/prospects-cli/internal/quota"
)

// Version is stamped into the User-Agent. Overridden at build time with
// -ldflags "-X github.com/EOEboh/prospects-cli/internal/config.Version=v0.2.0".
var Version = "dev"

// Config is the fully resolved runtime configuration. Everything comes from
// the environment; .env is loaded first for local dev and never wins over a
// real env var.
type Config struct {
	DBPath      string
	CacheDBPath string

	LogLevel  slog.Level
	LogFormat string // text | json

	// ContactEmail is embedded in the User-Agent of every outbound fetch.
	// Commands that touch the network refuse to run without it; see
	// RequireFetchable.
	ContactEmail string

	HTTPTimeout time.Duration
	CacheTTL    time.Duration
	Workers     int
	RatePerHost time.Duration

	WeightsPath string

	PlacesAPIKey string

	// PlacesMonthlyMax is an absolute per-SKU ceiling. Zero means "derive it
	// from the tier's free allowance", which is the default and what keeps a
	// fresh install free.
	PlacesMonthlyMax int

	// AllowPaidAPIs is the master switch for spending money. While false, no
	// configured ceiling may exceed the free allowance, so no combination of
	// other settings can produce a bill.
	AllowPaidAPIs bool

	// QuotaSafetyMargin scales free allowances down. The local counter can
	// drift below the provider's, so stopping short of the real limit is what
	// makes "free" actually hold.
	QuotaSafetyMargin float64

	MetaAdsToken   string
	MetaAdsEnabled bool
}

// Defaults documents every knob in one place. The .env.example file is
// generated from the same values by hand — keep them in sync.
const (
	DefaultDBPath      = "./prospect.db"
	DefaultCacheDBPath = "./cache.db"
	DefaultLogLevel    = "info"
	DefaultLogFormat   = "text"
	DefaultHTTPTimeout = 15 * time.Second
	DefaultCacheTTL    = 7 * 24 * time.Hour
	DefaultWorkers     = 5
	DefaultRatePerHost = time.Second
	DefaultWeightsPath = "./weights.yaml"

	// DefaultPlacesMax is 0: derive the ceiling from each SKU's free monthly
	// allowance rather than hardcoding a number that goes stale when Google
	// changes its tiers.
	DefaultPlacesMax = 0
)

// ErrNoContactEmail is returned by RequireFetchable. The tool identifies
// itself truthfully on every request, so there is no fallback User-Agent.
var ErrNoContactEmail = errors.New(
	"PROSPECT_USER_AGENT_EMAIL is required for commands that fetch websites: " +
		"the crawler identifies itself with a real contact address",
)

// Load resolves configuration from .env plus the environment. It collects all
// validation failures rather than reporting the first, so a fresh setup is
// fixed in one pass.
func Load(dotenvPath string) (*Config, error) {
	if err := LoadDotEnv(dotenvPath); err != nil {
		return nil, err
	}

	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	c := &Config{
		DBPath:       envStr("PROSPECT_DB_PATH", DefaultDBPath),
		CacheDBPath:  envStr("PROSPECT_CACHE_DB_PATH", DefaultCacheDBPath),
		LogFormat:    strings.ToLower(envStr("PROSPECT_LOG_FORMAT", DefaultLogFormat)),
		ContactEmail: strings.TrimSpace(os.Getenv("PROSPECT_USER_AGENT_EMAIL")),
		WeightsPath:  envStr("PROSPECT_WEIGHTS_PATH", DefaultWeightsPath),
		PlacesAPIKey: strings.TrimSpace(os.Getenv("GOOGLE_PLACES_API_KEY")),
		MetaAdsToken: strings.TrimSpace(os.Getenv("META_ADS_ACCESS_TOKEN")),
	}

	level, err := parseLevel(envStr("PROSPECT_LOG_LEVEL", DefaultLogLevel))
	fail(err)
	c.LogLevel = level

	if c.LogFormat != "text" && c.LogFormat != "json" {
		fail(fmt.Errorf("PROSPECT_LOG_FORMAT: want text or json, got %q", c.LogFormat))
	}

	c.HTTPTimeout, err = envDuration("PROSPECT_HTTP_TIMEOUT", DefaultHTTPTimeout)
	fail(err)
	c.CacheTTL, err = envDuration("PROSPECT_CACHE_TTL", DefaultCacheTTL)
	fail(err)
	c.RatePerHost, err = envDuration("PROSPECT_RATE_PER_HOST", DefaultRatePerHost)
	fail(err)
	c.Workers, err = envInt("PROSPECT_WORKERS", DefaultWorkers)
	fail(err)
	c.PlacesMonthlyMax, err = envInt("PROSPECT_PLACES_MONTHLY_MAX", DefaultPlacesMax)
	fail(err)
	c.AllowPaidAPIs, err = envBool("PROSPECT_ALLOW_PAID_APIS", false)
	fail(err)
	c.QuotaSafetyMargin, err = envFloat("PROSPECT_QUOTA_SAFETY_MARGIN", quota.DefaultSafetyMargin)
	fail(err)
	c.MetaAdsEnabled, err = envBool("PROSPECT_ENABLE_META_ADS", false)
	fail(err)

	if c.HTTPTimeout <= 0 {
		fail(errors.New("PROSPECT_HTTP_TIMEOUT must be positive: no unbounded requests"))
	}
	if c.Workers < 1 {
		fail(fmt.Errorf("PROSPECT_WORKERS must be >= 1, got %d", c.Workers))
	}
	if c.RatePerHost < 0 {
		fail(errors.New("PROSPECT_RATE_PER_HOST must not be negative"))
	}
	if c.CacheTTL < 0 {
		fail(errors.New("PROSPECT_CACHE_TTL must not be negative"))
	}
	if c.PlacesMonthlyMax < 0 {
		fail(errors.New("PROSPECT_PLACES_MONTHLY_MAX must not be negative (0 = derive from the free tier)"))
	}
	if c.QuotaSafetyMargin <= 0 || c.QuotaSafetyMargin > 1 {
		fail(fmt.Errorf("PROSPECT_QUOTA_SAFETY_MARGIN must be between 0 and 1, got %v", c.QuotaSafetyMargin))
	}
	if c.ContactEmail != "" && !strings.Contains(c.ContactEmail, "@") {
		fail(fmt.Errorf("PROSPECT_USER_AGENT_EMAIL: %q is not an email address", c.ContactEmail))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// RequireFetchable gates commands that make outbound HTTP requests.
// Zero-key commands (seed, score, list, export, brief) never call it.
func (c *Config) RequireFetchable() error {
	if c.ContactEmail == "" {
		return ErrNoContactEmail
	}
	return nil
}

// UserAgent identifies the tool and a real contact address, so an operator
// who sees us in their logs can reach a human.
func (c *Config) UserAgent() string {
	return fmt.Sprintf("prospect/%s (+mailto:%s)", Version, c.ContactEmail)
}

// PlacesEnabled and MetaAdsAvailable decide whether a key-gated source runs
// or degrades to a logged no-op. A missing key is never fatal.
func (c *Config) PlacesEnabled() bool    { return c.PlacesAPIKey != "" }
func (c *Config) MetaAdsAvailable() bool { return c.MetaAdsEnabled && c.MetaAdsToken != "" }

// QuotaLimits translates configuration into the ceilings the ledger enforces.
//
// PlacesMonthlyMax applies to every Places SKU rather than one, because the
// operator thinks in "calls I am willing to make", not in Google's SKU
// taxonomy. With AllowPaidAPIs false, each is still clamped to its own free
// allowance, so a single generous number cannot overspend a cheaper SKU.
func (c *Config) QuotaLimits() quota.Limits {
	limits := quota.Limits{
		AllowPaid:    c.AllowPaidAPIs,
		SafetyMargin: c.QuotaSafetyMargin,
	}
	if c.PlacesMonthlyMax > 0 {
		limits.Overrides = make(map[string]int)
		for _, sku := range quota.AllSKUs() {
			if sku.Provider == quota.ProviderPlaces {
				limits.Overrides[sku.Name] = c.PlacesMonthlyMax
			}
		}
	}
	return limits
}

// LogValue redacts credentials so the resolved config can be logged at debug
// level without leaking keys into a terminal or a log aggregator.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("db_path", c.DBPath),
		slog.String("cache_db_path", c.CacheDBPath),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.String("contact_email", redactEmail(c.ContactEmail)),
		slog.Duration("http_timeout", c.HTTPTimeout),
		slog.Duration("cache_ttl", c.CacheTTL),
		slog.Int("workers", c.Workers),
		slog.Duration("rate_per_host", c.RatePerHost),
		slog.String("weights_path", c.WeightsPath),
		slog.Bool("places_key_set", c.PlacesAPIKey != ""),
		slog.Int("places_monthly_max", c.PlacesMonthlyMax),
		slog.Bool("allow_paid_apis", c.AllowPaidAPIs),
		slog.Float64("quota_safety_margin", c.QuotaSafetyMargin),
		slog.Bool("meta_ads_enabled", c.MetaAdsEnabled),
		slog.Bool("meta_ads_token_set", c.MetaAdsToken != ""),
	)
}

func redactEmail(e string) string {
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" {
		return ""
	}
	return local[:1] + "***@" + domain
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a boolean", key, v)
	}
	return b, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a number", key, v)
	}
	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a duration (want 15s, 168h)", key, v)
	}
	return d, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("PROSPECT_LOG_LEVEL: want debug|info|warn|error, got %q", s)
	}
}
