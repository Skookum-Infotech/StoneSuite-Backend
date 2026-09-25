package controllers

import (
	"regexp"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
)

// intent aliases ai.Intent so classifyIntent's return type is exactly what
// Assistant.ForCaller consumes — one enum, not a controllers-side copy that
// could drift from what ai/adapter.go actually does with each value.
type intent = ai.Intent

const (
	// intentHelp is a question about how to use StoneSuite itself (screens,
	// menus, buttons) — answered from the help corpus only.
	intentHelp = ai.IntentHelp
	// intentData is a question about the caller's own CRM data (a record, a
	// field, a list) — answered from the records corpus only.
	intentData = ai.IntentData
	// intentMixed is a question with cues for both, or neither — answered
	// from both corpora, a narrower K each.
	intentMixed = ai.IntentMixed
)

// intentDataRecordTypeRe matches a CRM record type word, singular or plural.
var intentDataRecordTypeRe = regexp.MustCompile(`(?i)\b(lead|leads|prospect|prospects|customer|customers)\b`)

// intentDataMyOurRe matches the possessive "my"/"our" that, together with a
// data noun elsewhere in the question, signals the caller's own records
// ("my leads", "our customers in Texas").
var intentDataMyOurRe = regexp.MustCompile(`(?i)\b(my|our)\b`)

// intentDataFieldWordRe matches a CRM field name a data question commonly
// names.
var intentDataFieldWordRe = regexp.MustCompile(`(?i)\b(city|phone|email|address|status|owner|company|created|value|total)\b`)

// intentDataInterrogativeRe matches the question-opener words a data lookup
// or list request commonly starts with.
var intentDataInterrogativeRe = regexp.MustCompile(`(?i)\b(who|which|list|find)\b|show me`)

// intentHelpCueRe matches the "how do I use this app" phrasing a help
// question commonly carries.
var intentHelpCueRe = regexp.MustCompile(`(?i)\bhow (do|can) i\b|\bhow to\b|\bwhere (do|can|is)\b|\bwhat (is|does)\b|\bcan i\b|\bsteps\b|\bset up\b|\bconfigure\b|\benable\b|\bturn on\b|\binvite\b|\bcreate a\b|\bimport\b|\bexport\b|\bprint\b|\bemail a\b`)

// hasDataCue reports whether question carries a data-intent signal — see the
// package doc's cue list on classifyIntent.
func hasDataCue(question string) bool {
	if intentDataRecordTypeRe.MatchString(question) {
		return true
	}
	if recordNumberRe.MatchString(question) {
		return true
	}
	if intentDataInterrogativeRe.MatchString(question) {
		return true
	}
	if intentDataMyOurRe.MatchString(question) && (intentDataRecordTypeRe.MatchString(question) || intentDataFieldWordRe.MatchString(question)) {
		return true
	}
	if intentDataFieldWordRe.MatchString(question) {
		return true
	}
	return false
}

// hasHelpCue reports whether question carries a help-intent signal — see the
// package doc's cue list on classifyIntent.
func hasHelpCue(question string) bool {
	return intentHelpCueRe.MatchString(question)
}

// classifyIntent decides which corpus (or corpora) an ask should search:
// intentHelp (app how-to), intentData (the caller's own CRM records), or
// intentMixed (both cues present, or neither). prevUserQuestion is the
// caller's previous turn (empty for a single-turn ask or the first turn of a
// conversation) — used only when question itself carries neither cue, so a
// contextless short follow-up ("and its city?" after "what's lead LEAD-1's
// phone number") inherits the prior turn's leaning instead of defaulting to
// mixed.
func classifyIntent(question string, prevUserQuestion string) intent {
	data, help := hasDataCue(question), hasHelpCue(question)
	if !data && !help && prevUserQuestion != "" {
		data, help = hasDataCue(prevUserQuestion), hasHelpCue(prevUserQuestion)
	}
	switch {
	case data && !help:
		return intentData
	case help && !data:
		return intentHelp
	default:
		return intentMixed
	}
}

// previousUserQuestion returns the most recent user turn's text from history
// (oldest first), or "" if there is none — what classifyIntent's
// prevUserQuestion parameter expects for a contextless follow-up.
func previousUserQuestion(history []ragcore.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == ragcore.RoleUser {
			return history[i].Content
		}
	}
	return ""
}
