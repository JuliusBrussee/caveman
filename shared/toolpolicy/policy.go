// Package toolpolicy matches explicitly configured model-visible tool names.
// It never guesses server identity from result content.
package toolpolicy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Policy is a list of exact names or prefixes ending in one '*'. Empty disables it.
type Policy []string

// Parse accepts the JSON string array used by CAVEMAN_PRESERVE_TOOLS.
func Parse(raw string) (Policy, error) {
	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("preserve_tools must be a JSON string array")
	}
	if strings.TrimSpace(raw) == "null" {
		return nil, fmt.Errorf("preserve_tools must be an array, not null")
	}
	return p, p.Validate()
}

// Validate rejects empty identities and ambiguous wildcard syntax.
func (p Policy) Validate() error {
	for _, rule := range p {
		prefix := strings.TrimSuffix(rule, "*")
		if prefix == "" || strings.TrimSpace(rule) != rule || strings.ContainsAny(prefix, "*?[]\\ \t\r\n") {
			return fmt.Errorf("preserve_tools requires exact names or nonempty prefixes ending in one '*'")
		}
	}
	return nil
}

// Matches applies case-sensitive matching to the actual protocol tool name.
func (p Policy) Matches(name string) bool {
	for _, rule := range p {
		if strings.HasSuffix(rule, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(rule, "*")) {
				return true
			}
		} else if name == rule {
			return true
		}
	}
	return false
}

// Matcher is an immutable, comparable handle suitable for request metadata.
// NewMatcher snapshots the rules; callers cannot mutate its underlying policy.
type Matcher struct{ policy *Policy }

func NewMatcher(p Policy) Matcher {
	if len(p) == 0 {
		return Matcher{}
	}
	snapshot := append(Policy(nil), p...)
	return Matcher{policy: &snapshot}
}
func (m Matcher) Enabled() bool            { return m.policy != nil && len(*m.policy) > 0 }
func (m Matcher) Matches(name string) bool { return m.policy != nil && m.policy.Matches(name) }
