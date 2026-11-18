package cost

import (
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
)

// How a price rule picks its nodes.
const (
	MatchAll   = "all"   // every node
	MatchLabel = "label" // nodes with a label (and, when the value is given, that value)
	MatchName  = "name"  // nodes whose name matches a glob such as "gpu-*"
)

// MaxRulePriority bounds the priority of a rule.
const MaxRulePriority = 1000

var currencyPattern = regexp.MustCompile(`^[A-Z]{2,8}$`)

// Rule says what an hour of a node costs. It is the price an administrator typed in: the platform never guesses one, so a node no rule
// matches is "unpriced" and is never counted as free.
type Rule struct {
	ID          uint    `json:"id"`
	Name        string  `json:"name"`
	MatchKind   string  `json:"match_kind"`
	LabelKey    string  `json:"label_key,omitempty"`
	LabelValue  string  `json:"label_value,omitempty"`
	NamePattern string  `json:"name_pattern,omitempty"`
	HourlyPrice float64 `json:"hourly_price"`
	// Currency is a code such as USD or CNY; amounts of different currencies are never added together.
	Currency string `json:"currency"`
	// Priority orders overlapping rules: the higher wins; on a tie the more specific match (name over label over all) wins, then the lower id.
	Priority int `json:"priority"`
}

// Normalize trims the text fields and upper-cases the currency.
func (r *Rule) Normalize() {
	r.Name, r.LabelKey, r.LabelValue = strings.TrimSpace(r.Name), strings.TrimSpace(r.LabelKey), strings.TrimSpace(r.LabelValue)
	r.NamePattern, r.MatchKind = strings.TrimSpace(r.NamePattern), strings.TrimSpace(r.MatchKind)
	r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))
	if r.MatchKind == "" {
		r.MatchKind = MatchAll
	}
}

// Validate says what is wrong with a rule; call Normalize first.
func (r Rule) Validate() error {
	if r.Name == "" {
		return errors.New("a price rule needs a name")
	}
	switch r.MatchKind {
	case MatchAll:
	case MatchLabel:
		if r.LabelKey == "" {
			return errors.New("a label rule needs the label key")
		}
	case MatchName:
		if r.NamePattern == "" {
			return errors.New("a name rule needs a name pattern")
		}
		if _, err := path.Match(r.NamePattern, "x"); err != nil {
			return fmt.Errorf("the name pattern is not valid: %w", err)
		}
	default:
		return fmt.Errorf("match kind %q is not one of all, label, name", r.MatchKind)
	}
	if math.IsNaN(r.HourlyPrice) || math.IsInf(r.HourlyPrice, 0) || r.HourlyPrice < 0 {
		return errors.New("the hourly price must be a number, zero or more")
	}
	if !currencyPattern.MatchString(r.Currency) {
		return errors.New("the currency is a code of 2 to 8 letters such as USD or CNY")
	}
	if r.Priority < 0 || r.Priority > MaxRulePriority {
		return fmt.Errorf("the priority is between 0 and %d", MaxRulePriority)
	}
	return nil
}

// Matches reports whether the rule applies to the node.
func (r Rule) Matches(n Node) bool {
	switch r.MatchKind {
	case MatchLabel:
		v, ok := n.Labels[r.LabelKey]
		if !ok {
			// kube-state-metrics exports a key with every character that is not a letter, a digit or "_" turned into "_"
			v, ok = n.Labels[SanitizeLabelKey(r.LabelKey)]
		}
		return ok && (r.LabelValue == "" || v == r.LabelValue)
	case MatchName:
		ok, err := path.Match(r.NamePattern, n.Name)
		return err == nil && ok
	default:
		return true
	}
}

func (r Rule) specificity() int {
	switch r.MatchKind {
	case MatchName:
		return 2
	case MatchLabel:
		return 1
	}
	return 0
}

// PriceOf picks the rule that prices a node, if any.
func PriceOf(n Node, rules []Rule) (Rule, bool) {
	var matched []Rule
	for _, r := range rules {
		if r.Matches(n) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return Rule{}, false
	}
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.specificity() != b.specificity() {
			return a.specificity() > b.specificity()
		}
		return a.ID < b.ID
	})
	return matched[0], true
}
