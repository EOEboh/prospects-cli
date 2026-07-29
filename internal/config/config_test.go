package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EOEboh/prospects-cli/internal/quota"
)

func TestParseDotEnv(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "plain assignment",
			input: "FOO=bar\nBAZ=qux\n",
			want:  map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:  "comments and blank lines ignored",
			input: "# a comment\n\nFOO=bar\n\n  # indented comment\n",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "export prefix stripped",
			input: "export FOO=bar\n",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "double quotes expand escapes",
			input: "FOO=\"line1\\nline2\"\n",
			want:  map[string]string{"FOO": "line1\nline2"},
		},
		{
			name:  "single quotes stay literal",
			input: "FOO='line1\\nline2'\n",
			want:  map[string]string{"FOO": `line1\nline2`},
		},
		{
			name:  "inline comment stripped from unquoted value",
			input: "FOO=bar # trailing note\n",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "hash inside quotes is kept",
			input: `FOO="bar # not a comment"` + "\n",
			want:  map[string]string{"FOO": "bar # not a comment"},
		},
		{
			name:  "value containing equals",
			input: "TOKEN=abc=def==\n",
			want:  map[string]string{"TOKEN": "abc=def=="},
		},
		{
			name:  "empty value",
			input: "FOO=\n",
			want:  map[string]string{"FOO": ""},
		},
		{
			name:  "whitespace around key and value",
			input: "  FOO  =  bar  \n",
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:    "missing equals",
			input:   "JUST_A_KEY\n",
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   "=value\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDotEnv(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDotEnv(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDotEnv(%q): %v", tc.input, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("key %q: got %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// A real env var must beat the .env file, so a shell export overrides local
// config without editing it.
func TestLoadDotEnvDoesNotOverrideRealEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeFile(t, path, "PROSPECT_LOG_LEVEL=debug\nPROSPECT_WORKERS=9\n")

	t.Setenv("PROSPECT_LOG_LEVEL", "warn") // already in the environment
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	if got := envStr("PROSPECT_LOG_LEVEL", ""); got != "warn" {
		t.Errorf("real env lost to .env: got %q, want warn", got)
	}
	if got := envStr("PROSPECT_WORKERS", ""); got != "9" {
		t.Errorf("unset var not filled from .env: got %q, want 9", got)
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("missing .env should be tolerated, got %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DBPath", cfg.DBPath, DefaultDBPath},
		{"CacheDBPath", cfg.CacheDBPath, DefaultCacheDBPath},
		{"LogLevel", cfg.LogLevel, slog.LevelInfo},
		{"LogFormat", cfg.LogFormat, DefaultLogFormat},
		{"HTTPTimeout", cfg.HTTPTimeout, DefaultHTTPTimeout},
		{"CacheTTL", cfg.CacheTTL, DefaultCacheTTL},
		{"Workers", cfg.Workers, DefaultWorkers},
		{"RatePerHost", cfg.RatePerHost, DefaultRatePerHost},
		{"PlacesMonthlyMax", cfg.PlacesMonthlyMax, DefaultPlacesMax},
		{"AllowPaidAPIs", cfg.AllowPaidAPIs, false},
		{"QuotaSafetyMargin", cfg.QuotaSafetyMargin, quota.DefaultSafetyMargin},
		{"MetaAdsEnabled", cfg.MetaAdsEnabled, false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// Key-gated sources stay off until their keys exist. A missing key is
	// never an error.
	if cfg.PlacesEnabled() || cfg.MetaAdsAvailable() {
		t.Error("paid sources should be disabled with no keys set")
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // substring the message must contain
	}{
		{
			name:    "bad log level",
			env:     map[string]string{"PROSPECT_LOG_LEVEL": "loud"},
			wantErr: "PROSPECT_LOG_LEVEL",
		},
		{
			name:    "bad log format",
			env:     map[string]string{"PROSPECT_LOG_FORMAT": "xml"},
			wantErr: "PROSPECT_LOG_FORMAT",
		},
		{
			name:    "non-integer workers",
			env:     map[string]string{"PROSPECT_WORKERS": "many"},
			wantErr: "not an integer",
		},
		{
			name:    "zero workers",
			env:     map[string]string{"PROSPECT_WORKERS": "0"},
			wantErr: "must be >= 1",
		},
		{
			name:    "bad duration",
			env:     map[string]string{"PROSPECT_HTTP_TIMEOUT": "15 seconds"},
			wantErr: "not a duration",
		},
		{
			name:    "zero timeout rejected: no unbounded requests",
			env:     map[string]string{"PROSPECT_HTTP_TIMEOUT": "0s"},
			wantErr: "must be positive",
		},
		{
			name:    "non-boolean flag",
			env:     map[string]string{"PROSPECT_ENABLE_META_ADS": "yes please"},
			wantErr: "not a boolean",
		},
		{
			name:    "contact email without @",
			env:     map[string]string{"PROSPECT_USER_AGENT_EMAIL": "not-an-email"},
			wantErr: "not an email address",
		},
		{
			name:    "negative monthly ceiling",
			env:     map[string]string{"PROSPECT_PLACES_MONTHLY_MAX": "-1"},
			wantErr: "must not be negative",
		},
		{
			name:    "safety margin above 1 would exceed the free tier",
			env:     map[string]string{"PROSPECT_QUOTA_SAFETY_MARGIN": "1.5"},
			wantErr: "must be between 0 and 1",
		},
		{
			name:    "zero safety margin",
			env:     map[string]string{"PROSPECT_QUOTA_SAFETY_MARGIN": "0"},
			wantErr: "must be between 0 and 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load(filepath.Join(t.TempDir(), "absent"))
			if err == nil {
				t.Fatalf("Load with %v: want error", tc.env)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// A fresh setup with several mistakes should report all of them at once
// rather than one per run.
func TestLoadReportsAllErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("PROSPECT_LOG_LEVEL", "loud")
	t.Setenv("PROSPECT_WORKERS", "many")
	t.Setenv("PROSPECT_HTTP_TIMEOUT", "soon")

	_, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"PROSPECT_LOG_LEVEL", "PROSPECT_WORKERS", "PROSPECT_HTTP_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("combined error missing %s:\n%v", want, err)
		}
	}
}

// The safety property: a fresh install cannot spend money, and no amount of
// PROSPECT_PLACES_MONTHLY_MAX changes that without the explicit opt-in.
func TestQuotaLimitsStayFreeByDefault(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantCeiling int
		wantClamped bool
	}{
		{
			name:        "defaults derive from the free tier",
			env:         nil,
			wantCeiling: 900, // enterprise 1000 * 0.9
		},
		{
			name:        "a huge ceiling is clamped without the opt-in",
			env:         map[string]string{"PROSPECT_PLACES_MONTHLY_MAX": "100000"},
			wantCeiling: 900,
			wantClamped: true,
		},
		{
			name: "the opt-in honors the configured ceiling",
			env: map[string]string{
				"PROSPECT_PLACES_MONTHLY_MAX": "100000",
				"PROSPECT_ALLOW_PAID_APIS":    "true",
			},
			wantCeiling: 100000,
		},
		{
			name:        "a tighter ceiling than free is always honored",
			env:         map[string]string{"PROSPECT_PLACES_MONTHLY_MAX": "50"},
			wantCeiling: 50,
		},
		{
			name:        "a tighter safety margin tightens the ceiling",
			env:         map[string]string{"PROSPECT_QUOTA_SAFETY_MARGIN": "0.5"},
			wantCeiling: 500,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := Load(filepath.Join(t.TempDir(), "absent"))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			limits := cfg.QuotaLimits()
			sku := quota.SKUTextSearchEnterpise
			if got := limits.Ceiling(sku); got != tc.wantCeiling {
				t.Errorf("Ceiling(%s) = %d, want %d", sku.Name, got, tc.wantCeiling)
			}
			if _, clamped := limits.Clamped(sku); clamped != tc.wantClamped {
				t.Errorf("Clamped = %v, want %v", clamped, tc.wantClamped)
			}
		})
	}
}

// One configured number must not lift a cheaper SKU past its own allowance.
func TestQuotaLimitsClampPerSKU(t *testing.T) {
	clearEnv(t)
	t.Setenv("PROSPECT_PLACES_MONTHLY_MAX", "8000")

	cfg, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	limits := cfg.QuotaLimits()

	// 8000 is inside the Essentials allowance but well past Enterprise's.
	if got := limits.Ceiling(quota.SKUTextSearchIDsOnly); got != 8000 {
		t.Errorf("essentials ceiling = %d, want 8000", got)
	}
	if got := limits.Ceiling(quota.SKUTextSearchEnterpise); got != 900 {
		t.Errorf("enterprise ceiling = %d, want 900 (clamped to its own free tier)", got)
	}
}

func TestRequireFetchable(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The crawler identifies itself truthfully or does not run.
	if err := cfg.RequireFetchable(); err == nil {
		t.Error("fetching without a contact email should be refused")
	}

	cfg.ContactEmail = "me@example.com"
	if err := cfg.RequireFetchable(); err != nil {
		t.Errorf("RequireFetchable with an email: %v", err)
	}
	if got := cfg.UserAgent(); !strings.Contains(got, "me@example.com") {
		t.Errorf("UserAgent %q must carry the contact address", got)
	}
}

func TestLogValueRedactsSecrets(t *testing.T) {
	cfg := &Config{
		ContactEmail: "operator@example.com",
		PlacesAPIKey: "AIza-super-secret",
		MetaAdsToken: "EAAG-super-secret",
		HTTPTimeout:  time.Second,
	}
	rendered := cfg.LogValue().String()

	for _, secret := range []string{"AIza-super-secret", "EAAG-super-secret", "operator@"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("LogValue leaked %q:\n%s", secret, rendered)
		}
	}
	// Presence is still reportable; the value is not.
	if !strings.Contains(rendered, "places_key_set=true") {
		t.Errorf("LogValue should report that a key is set:\n%s", rendered)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// clearEnv unsets every variable Load reads, so a test never inherits the
// developer's real shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PROSPECT_DB_PATH", "PROSPECT_CACHE_DB_PATH", "PROSPECT_LOG_LEVEL",
		"PROSPECT_LOG_FORMAT", "PROSPECT_USER_AGENT_EMAIL", "PROSPECT_HTTP_TIMEOUT",
		"PROSPECT_CACHE_TTL", "PROSPECT_WORKERS", "PROSPECT_RATE_PER_HOST",
		"PROSPECT_WEIGHTS_PATH", "GOOGLE_PLACES_API_KEY", "PROSPECT_PLACES_MONTHLY_MAX",
		"PROSPECT_ALLOW_PAID_APIS", "PROSPECT_QUOTA_SAFETY_MARGIN",
		"META_ADS_ACCESS_TOKEN", "PROSPECT_ENABLE_META_ADS",
	} {
		t.Setenv(k, "")
	}
}
