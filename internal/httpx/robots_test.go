package httpx

import (
	"strings"
	"testing"
	"time"
)

const ourAgent = "prospect/1.0 (+mailto:me@example.com)"

func TestRobotsAllows(t *testing.T) {
	tests := []struct {
		name    string
		robots  string
		path    string
		agent   string
		allowed bool
	}{
		{
			name:    "no rules allows everything",
			robots:  "",
			path:    "/contact",
			allowed: true,
		},
		{
			name:    "disallow all",
			robots:  "User-agent: *\nDisallow: /",
			path:    "/contact",
			allowed: false,
		},
		{
			name:    "disallow all blocks the root too",
			robots:  "User-agent: *\nDisallow: /",
			path:    "/",
			allowed: false,
		},
		{
			name:    "empty disallow allows everything",
			robots:  "User-agent: *\nDisallow:",
			path:    "/contact",
			allowed: true,
		},
		{
			name:    "unrelated directory blocked",
			robots:  "User-agent: *\nDisallow: /admin",
			path:    "/contact",
			allowed: true,
		},
		{
			name:    "matching directory blocked",
			robots:  "User-agent: *\nDisallow: /admin",
			path:    "/admin/login",
			allowed: false,
		},

		// The longest match wins, which is what lets a site block a directory
		// and re-permit one page inside it.
		{
			name:    "longer allow beats shorter disallow",
			robots:  "User-agent: *\nDisallow: /private\nAllow: /private/contact",
			path:    "/private/contact",
			allowed: true,
		},
		{
			name:    "longer disallow beats shorter allow",
			robots:  "User-agent: *\nAllow: /docs\nDisallow: /docs/internal",
			path:    "/docs/internal/page",
			allowed: false,
		},
		{
			name:    "allow wins an equal-length tie",
			robots:  "User-agent: *\nDisallow: /page\nAllow: /page",
			path:    "/page",
			allowed: true,
		},

		// Wildcards.
		{
			name:    "star matches any sequence",
			robots:  "User-agent: *\nDisallow: /*.pdf",
			path:    "/files/report.pdf",
			allowed: false,
		},
		{
			name:    "star pattern does not over-match",
			robots:  "User-agent: *\nDisallow: /*.pdf",
			path:    "/files/report.html",
			allowed: true,
		},
		{
			name:    "dollar anchors the end",
			robots:  "User-agent: *\nDisallow: /contact$",
			path:    "/contact",
			allowed: false,
		},
		{
			name:    "dollar anchor does not match a longer path",
			robots:  "User-agent: *\nDisallow: /contact$",
			path:    "/contact/form",
			allowed: true,
		},
		{
			name:    "star and dollar together",
			robots:  "User-agent: *\nDisallow: /*/private$",
			path:    "/team/private",
			allowed: false,
		},

		// Agent selection.
		{
			name:    "our named group applies",
			robots:  "User-agent: prospect\nDisallow: /\n\nUser-agent: *\nAllow: /",
			path:    "/contact",
			allowed: false,
		},
		{
			name:    "another crawler's group does not apply to us",
			robots:  "User-agent: googlebot\nDisallow: /\n\nUser-agent: *\nAllow: /",
			path:    "/contact",
			allowed: true,
		},
		{
			name:    "agent matching is case-insensitive",
			robots:  "User-agent: PROSPECT\nDisallow: /admin",
			path:    "/admin",
			allowed: false,
		},
		{
			name:    "falls back to the wildcard group",
			robots:  "User-agent: googlebot\nAllow: /\n\nUser-agent: *\nDisallow: /admin",
			path:    "/admin",
			allowed: false,
		},
		{
			name:    "several agents share one group",
			robots:  "User-agent: googlebot\nUser-agent: prospect\nDisallow: /admin",
			path:    "/admin",
			allowed: false,
		},

		// Formatting tolerance.
		{
			name:    "comments are ignored",
			robots:  "# a comment\nUser-agent: * # inline\nDisallow: /admin # here\n",
			path:    "/admin",
			allowed: false,
		},
		{
			name:    "field names are case-insensitive",
			robots:  "USER-AGENT: *\nDISALLOW: /admin",
			path:    "/admin",
			allowed: false,
		},
		{
			name:    "extra whitespace tolerated",
			robots:  "  User-agent :   *  \n  Disallow :  /admin  ",
			path:    "/admin",
			allowed: false,
		},
		{
			name:    "rules before any user-agent are ignored",
			robots:  "Disallow: /\nUser-agent: *\nAllow: /",
			path:    "/contact",
			allowed: true,
		},
		{
			name:    "unparsable lines are skipped",
			robots:  "this is not a directive\nUser-agent: *\nDisallow: /admin",
			path:    "/admin",
			allowed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := tc.agent
			if agent == "" {
				agent = ourAgent
			}
			r := ParseRobots(strings.NewReader(tc.robots))
			if got := r.Allows(agent, tc.path); got != tc.allowed {
				t.Errorf("Allows(%q) = %v, want %v\nrobots.txt:\n%s", tc.path, got, tc.allowed, tc.robots)
			}
		})
	}
}

// A missing robots.txt must not block a fetch.
func TestNilRobotsAllowsEverything(t *testing.T) {
	var r *Robots
	if !r.Allows(ourAgent, "/anything") {
		t.Error("a nil *Robots must allow everything")
	}
	if r.CrawlDelay(ourAgent) != 0 {
		t.Error("a nil *Robots must not impose a delay")
	}
}

// "Disallowed" with no explanation is not auditable, and the reason is stored
// as a signal.
func TestMatchedRuleExplainsTheDecision(t *testing.T) {
	r := ParseRobots(strings.NewReader("User-agent: *\nDisallow: /private"))
	got := r.MatchedRule(ourAgent, "/private/page")
	if got != "Disallow: /private" {
		t.Errorf("MatchedRule = %q, want %q", got, "Disallow: /private")
	}
	if r.MatchedRule(ourAgent, "/contact") != "" {
		t.Error("an unmatched path should report no rule")
	}
}

func TestCrawlDelay(t *testing.T) {
	tests := []struct {
		name   string
		robots string
		want   time.Duration
	}{
		{"none", "User-agent: *\nDisallow: /admin", 0},
		{"integer seconds", "User-agent: *\nCrawl-delay: 5", 5 * time.Second},
		{"fractional seconds", "User-agent: *\nCrawl-delay: 2.5", 2500 * time.Millisecond},
		{"invalid ignored", "User-agent: *\nCrawl-delay: soon", 0},
		{"negative ignored", "User-agent: *\nCrawl-delay: -5", 0},
		{"from our own group", "User-agent: *\nCrawl-delay: 1\n\nUser-agent: prospect\nCrawl-delay: 9", 9 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseRobots(strings.NewReader(tc.robots))
			if got := r.CrawlDelay(ourAgent); got != tc.want {
				t.Errorf("CrawlDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProductToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"prospect/1.0 (+mailto:me@example.com)", "prospect"},
		{"prospect", "prospect"},
		{"Prospect/dev", "prospect"},
		{"some bot", "some"},
	}
	for _, tc := range tests {
		if got := productToken(tc.in); got != tc.want {
			t.Errorf("productToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A group is closed by its first rule, so a later user-agent line starts a new
// group rather than joining the previous one.
func TestGroupBoundaries(t *testing.T) {
	const robots = `
User-agent: googlebot
Disallow: /

User-agent: prospect
Disallow: /admin
`
	r := ParseRobots(strings.NewReader(robots))
	if !r.Allows(ourAgent, "/contact") {
		t.Error("/contact should be allowed: the blanket disallow belongs to googlebot")
	}
	if r.Allows(ourAgent, "/admin") {
		t.Error("/admin should be blocked by our own group")
	}
}
