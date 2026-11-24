// The subscriptions registry: a YAML config file describing which clients
// receive which webhook notifications. The file holds one list of
// subscription maps; the parser below understands exactly that subset of
// YAML (a top-level `subscriptions:` key whose items are `- key: value`
// scalars), which keeps the module's dependency set at the pinned NATS and
// SQLite clients. The registry is hot-reloaded on SIGHUP: the running set
// is swapped atomically and pending work of subscriptions that disappeared
// is dropped.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// subscription is one client registration.
type subscription struct {
	ID            string // stable string id
	Network       string // network subject token or "*"
	EventType     string // block | transaction | "*"
	TxTo          string // optional exact-address filter on the tx "to"
	Confirmations int64  // deliver when head >= event height + N; 0 = immediate
	URL           string // HTTPS POST target
	Secret        string // HMAC-SHA256 key for X-Webhook-Signature
}

// MatchesNetwork reports whether the subscription covers network n.
func (s *subscription) MatchesNetwork(n string) bool {
	return s.Network == "*" || s.Network == n
}

// MatchesEventType reports whether the subscription covers event type t.
func (s *subscription) MatchesEventType(t string) bool {
	return s.EventType == "*" || s.EventType == t
}

// registry is the atomically-swappable set of live subscriptions.
type registry struct {
	mu   sync.RWMutex
	subs []subscription
}

// newRegistry pins the first loaded set.
func newRegistry(subs []subscription) *registry {
	return &registry{subs: subs}
}

// Swap replaces the live set.
func (r *registry) Swap(subs []subscription) {
	r.mu.Lock()
	r.subs = subs
	r.mu.Unlock()
}

// Snapshot returns a copy of the live set.
func (r *registry) Snapshot() []subscription {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]subscription, len(r.subs))
	copy(out, r.subs)
	return out
}

// Find returns the subscription with the given id, or nil.
func (r *registry) Find(id string) *subscription {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.subs {
		if r.subs[i].ID == id {
			return &r.subs[i]
		}
	}
	return nil
}

// IDs returns the live subscription ids.
func (r *registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, len(r.subs))
	for i := range r.subs {
		ids[i] = r.subs[i].ID
	}
	return ids
}

// loadSubscriptions reads and validates the config file.
func loadSubscriptions(path string) ([]subscription, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	subs, err := parseSubscriptions(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("%s: no subscriptions found", path)
	}
	return subs, nil
}

// parseSubscriptions understands the config subset: an optional document
// with a single top-level `subscriptions:` key holding a list of flat
// scalar maps. Quoted or bare scalar values, blank lines and full-line or
// trailing comments are handled; nesting beyond the list is not.
func parseSubscriptions(text string) ([]subscription, error) {
	var subs []subscription
	seen := map[string]bool{}
	inList := false
	cur := -1 // index into subs of the item being read

	fail := func(line int, msg string) error {
		return fmt.Errorf("line %d: %s", line+1, msg)
	}
	apply := func(line int, s *subscription, key, rawValue string) error {
		value, err := scalarValue(rawValue)
		if err != nil {
			return fail(line, fmt.Sprintf("%s: %v", key, err))
		}
		switch key {
		case "id":
			if value == "" {
				return fail(line, "id must not be empty")
			}
			s.ID = value
		case "network":
			if value == "" {
				return fail(line, "network must not be empty (use \"*\" for all)")
			}
			s.Network = value
		case "event_type":
			if value != "block" && value != "transaction" && value != "*" {
				return fail(line, fmt.Sprintf("event_type %q must be block, transaction or \"*\"", value))
			}
			s.EventType = value
		case "tx_to":
			s.TxTo = value
		case "confirmations":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return fail(line, fmt.Sprintf("confirmations %q must be a non-negative integer", value))
			}
			s.Confirmations = n
		case "url":
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				return fail(line, fmt.Sprintf("url %q must be an http or https URL", value))
			}
			s.URL = value
		case "secret":
			s.Secret = value
		default:
			return fail(line, fmt.Sprintf("unknown subscription key %q", key))
		}
		return nil
	}

	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := line != "" && (line[0] == ' ' || line[0] == '\t')
		if !indented {
			key, value, ok := cutKV(trimmed)
			if !ok {
				return nil, fail(i, "not a key: value line")
			}
			if key != "subscriptions" || value != "" {
				return nil, fail(i, fmt.Sprintf("top-level key %q is not supported (only the subscriptions list)", key))
			}
			inList = true
			continue
		}
		if !inList {
			return nil, fail(i, "indented line before the subscriptions list")
		}
		if trimmed == "-" || strings.HasPrefix(trimmed, "- ") {
			subs = append(subs, subscription{Network: "*", EventType: "*"})
			cur = len(subs) - 1
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if rest == "" {
				continue
			}
			key, value, ok := cutKV(rest)
			if !ok {
				return nil, fail(i, "list item is not a key: value pair")
			}
			if err := apply(i, &subs[cur], key, value); err != nil {
				return nil, err
			}
			continue
		}
		if cur < 0 {
			return nil, fail(i, "list item content before any \"- \" item")
		}
		key, value, ok := cutKV(trimmed)
		if !ok {
			return nil, fail(i, "not a key: value line")
		}
		if err := apply(i, &subs[cur], key, value); err != nil {
			return nil, err
		}
	}

	for i := range subs {
		s := &subs[i]
		if s.ID == "" {
			return nil, fmt.Errorf("subscription %d has no id", i+1)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("duplicate subscription id %q", s.ID)
		}
		seen[s.ID] = true
		if s.URL == "" {
			return nil, fmt.Errorf("subscription %q has no url", s.ID)
		}
		if s.TxTo != "" && s.EventType == "block" {
			return nil, fmt.Errorf("subscription %q: tx_to filter requires event_type transaction", s.ID)
		}
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("no subscriptions found")
	}
	return subs, nil
}

// cutKV splits "key: value" at the first colon; ok is false when no colon
// or empty key. The value keeps its raw form (quotes, trailing comment).
func cutKV(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(k)
	if key == "" {
		return "", "", false
	}
	return key, v, true
}

// scalarValue strips a trailing comment and one layer of quotes from a
// raw value.
func scalarValue(raw string) (string, error) {
	v := raw
	// A trailing comment must be preceded by whitespace to avoid cutting
	// URLs at a "#fragment".
	if cut := strings.Index(v, " #"); cut >= 0 {
		v = v[:cut]
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return v, nil
}
