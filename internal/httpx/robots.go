package httpx

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// Robots is a parsed robots.txt, following RFC 9309.
//
// The zero value allows everything, which is the correct reading of a missing
// or unreachable robots.txt.
type Robots struct {
	groups []robotsGroup
}

type robotsGroup struct {
	agents     []string
	rules      []robotsRule
	crawlDelay time.Duration
}

type robotsRule struct {
	pattern string
	allow   bool
}

// ParseRobots reads a robots.txt. It never fails: a malformed file is treated
// as the permissive rules it does contain, matching how the spec says to treat
// unparsable lines.
func ParseRobots(r io.Reader) *Robots {
	robots := &Robots{}

	var current *robotsGroup
	// Consecutive user-agent lines share one group; the first rule after them
	// closes the agent list.
	startingNewGroup := true

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			if value == "" {
				continue
			}
			if !startingNewGroup || current == nil {
				robots.groups = append(robots.groups, robotsGroup{})
				current = &robots.groups[len(robots.groups)-1]
				startingNewGroup = true
			}
			current.agents = append(current.agents, strings.ToLower(value))

		case "allow", "disallow":
			if current == nil {
				// Rules before any user-agent line have no group; ignore them.
				continue
			}
			startingNewGroup = false
			// "Disallow:" with an empty value allows everything, and is not a
			// rule to match against.
			if value == "" {
				if field == "disallow" {
					current.rules = append(current.rules, robotsRule{pattern: "/", allow: true})
				}
				continue
			}
			current.rules = append(current.rules, robotsRule{pattern: value, allow: field == "allow"})

		case "crawl-delay":
			if current == nil {
				continue
			}
			startingNewGroup = false
			if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
				current.crawlDelay = time.Duration(secs * float64(time.Second))
			}
		}
	}
	return robots
}

// Allows reports whether userAgent may fetch path.
//
// A nil *Robots allows everything: no robots.txt means no restrictions.
func (r *Robots) Allows(userAgent, path string) bool {
	allowed, _ := r.check(userAgent, path)
	return allowed
}

// MatchedRule returns the rule that decided a path, for the record we keep
// when a fetch is skipped. "Disallowed" with no explanation is not auditable.
func (r *Robots) MatchedRule(userAgent, path string) string {
	_, rule := r.check(userAgent, path)
	return rule
}

func (r *Robots) check(userAgent, path string) (bool, string) {
	if r == nil || len(r.groups) == 0 {
		return true, ""
	}
	group := r.groupFor(userAgent)
	if group == nil {
		return true, ""
	}
	if path == "" {
		path = "/"
	}

	// RFC 9309: the most specific (longest) matching pattern wins, and Allow
	// wins a tie. That tie-break is what lets a site disallow a directory and
	// re-permit one page inside it.
	best := -1
	allowed := true
	matched := ""
	for _, rule := range group.rules {
		if !matchRobotsPattern(rule.pattern, path) {
			continue
		}
		length := len(rule.pattern)
		if length > best || (length == best && rule.allow) {
			best = length
			allowed = rule.allow
			matched = ruleLabel(rule)
		}
	}
	return allowed, matched
}

func ruleLabel(r robotsRule) string {
	if r.allow {
		return "Allow: " + r.pattern
	}
	return "Disallow: " + r.pattern
}

// CrawlDelay is the delay this site asks for, or zero if it asks for none.
// Callers take the larger of this and their own configured rate.
func (r *Robots) CrawlDelay(userAgent string) time.Duration {
	if r == nil {
		return 0
	}
	if g := r.groupFor(userAgent); g != nil {
		return g.crawlDelay
	}
	return 0
}

// groupFor picks the group whose agent token best matches ours. An exact or
// prefix match beats the "*" wildcard, and the longest such match wins.
func (r *Robots) groupFor(userAgent string) *robotsGroup {
	token := productToken(userAgent)

	var best *robotsGroup
	bestLen := -1
	var wildcard *robotsGroup

	for i := range r.groups {
		g := &r.groups[i]
		for _, agent := range g.agents {
			if agent == "*" {
				if wildcard == nil {
					wildcard = g
				}
				continue
			}
			// RFC 9309 matches the product token case-insensitively, by prefix.
			if strings.HasPrefix(token, agent) && len(agent) > bestLen {
				best, bestLen = g, len(agent)
			}
		}
	}
	if best != nil {
		return best
	}
	return wildcard
}

// productToken extracts the comparable name from a User-Agent string:
// "prospect/1.0 (+mailto:me@example.com)" becomes "prospect".
func productToken(userAgent string) string {
	token := strings.ToLower(strings.TrimSpace(userAgent))
	if i := strings.IndexAny(token, "/ "); i > 0 {
		token = token[:i]
	}
	return token
}

// matchRobotsPattern implements robots.txt path matching, where "*" stands for
// any sequence and a trailing "$" anchors the end.
func matchRobotsPattern(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	segments := strings.Split(pattern, "*")

	// No wildcard: a plain prefix match, anchored at the end if "$" was given.
	if len(segments) == 1 {
		if anchored {
			return path == segments[0]
		}
		return strings.HasPrefix(path, segments[0])
	}

	if !strings.HasPrefix(path, segments[0]) {
		return false
	}
	pos := len(segments[0])

	for _, seg := range segments[1 : len(segments)-1] {
		if seg == "" {
			continue
		}
		i := strings.Index(path[pos:], seg)
		if i < 0 {
			return false
		}
		pos += i + len(seg)
	}

	last := segments[len(segments)-1]
	if last == "" {
		// Pattern ended with "*": anything remaining matches, unless the end
		// was also anchored, which only a bare "*$" would do.
		return !anchored || pos <= len(path)
	}
	if anchored {
		return strings.HasSuffix(path[pos:], last)
	}
	return strings.Contains(path[pos:], last)
}
