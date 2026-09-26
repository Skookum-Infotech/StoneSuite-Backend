package main

import "strings"

// placeholderKeys are the tokens resolvePlaceholders replaces, resolved once
// at startup from live tenant data (see fetchPlaceholders in client.go).
const (
	placeholderLeadNumber    = "{{lead.number}}"
	placeholderLeadCity      = "{{lead.city}}"
	placeholderLeadName      = "{{lead.name}}"
	placeholderCountLead     = "{{counts.lead}}"
	placeholderCountCustomer = "{{counts.customer}}"
	placeholderCountProspect = "{{counts.prospect}}"
)

// resolvePlaceholders replaces every {{...}} token in s with its value from
// ph. A token with no entry in ph is left as-is (surfaces as an obviously
// wrong answer/expectation rather than silently vanishing).
func resolvePlaceholders(s string, ph map[string]string) string {
	for token, val := range ph {
		s = strings.ReplaceAll(s, token, val)
	}
	return s
}

// resolveCase returns a copy of c with every placeholder resolved in its
// question and all expectation string fields — expectations like
// "You have {{counts.customer}} customers" need the same substitution as the
// question itself.
func resolveCase(c testCase, ph map[string]string) testCase {
	c.Question = resolvePlaceholders(c.Question, ph)
	c.ExpectContains = resolveAll(c.ExpectContains, ph)
	c.ExpectAny = resolveAll(c.ExpectAny, ph)
	c.ExpectNotContains = resolveAll(c.ExpectNotContains, ph)
	return c
}

// resolveAll applies resolvePlaceholders to every element of ss.
func resolveAll(ss []string, ph map[string]string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = resolvePlaceholders(s, ph)
	}
	return out
}
