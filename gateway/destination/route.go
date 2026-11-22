// The route half of the binding: the concrete NATS subject of one
// published event and the NATS subject-pattern matching the stream
// subjects check uses (MESSAGE_CONTRACT section 7: subjects are
// subject_prefix + "." + family + "." + kind over the closed registry;
// a full record and its parts go to one subject of their logical event).
package destination

import (
	"fmt"
	"strings"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Subject returns the concrete subject of one logical event:
// prefix + "." + family + "." + kind, for example
// "evm.v3.eth.tx.applied". Every fragment and every part of the record
// publishes to this one subject.
func Subject(prefix string, family proto.Family, kind proto.Kind) (string, error) {
	if err := ValidateSubjectPrefix(prefix); err != nil {
		return "", err
	}
	if !family.Valid() || !kind.Valid() {
		return "", fmt.Errorf("%w: family %s or kind %s is not in registry v1",
			ErrDescriptor, family, kind)
	}
	return prefix + "." + family.String() + "." + kind.String(), nil
}

// SubjectMatches reports whether the concrete subject matches one NATS
// subject pattern: "*" matches exactly one token, ">" matches one or more
// trailing tokens, and every other token must compare equal. The stream
// subjects list of StreamInfo uses these patterns.
func SubjectMatches(pattern, subject string) bool {
	if pattern == "" || subject == "" {
		return false
	}
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, tok := range pt {
		if tok == ">" {
			// ">" is only valid as the last token; as such it matches
			// one or more remaining subject tokens.
			return i > 0 && len(st) > i
		}
		if i >= len(st) {
			return false
		}
		if tok != "*" && tok != st[i] {
			return false
		}
	}
	return len(pt) == len(st)
}

// RouteCovered reports whether every concrete subject is covered by at
// least one pattern of the stream subjects list.
func RouteCovered(patterns []string, subjects ...string) bool {
	for _, s := range subjects {
		matched := false
		for _, p := range patterns {
			if SubjectMatches(p, s) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
