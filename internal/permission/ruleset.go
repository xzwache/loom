package permission

import "errors"

// RuleSet is the allow, ask and deny rules from the settings.
type RuleSet struct {
	allow rules
	ask   rules
	deny  rules
}

func newRuleSet(s Settings) (RuleSet, error) {
	var errs []error
	parse := func(raw []string) rules {
		var parsed rules
		for _, s := range raw {
			r, err := ParseRule(s)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			parsed = append(parsed, r)
		}
		return parsed
	}
	rs := RuleSet{allow: parse(s.Allow), ask: parse(s.Ask), deny: parse(s.Deny)}
	return rs, errors.Join(errs...)
}

type rules []Rule

// first returns the first rule for tool that fits.
func (rs rules) first(tool string, fits func(Rule) bool) (Rule, bool) {
	for _, r := range rs {
		if r.covers(tool) && fits(r) {
			return r, true
		}
	}
	return Rule{}, false
}
