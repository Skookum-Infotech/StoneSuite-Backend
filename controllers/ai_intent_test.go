package controllers

import "testing"

func TestClassifyIntent(t *testing.T) {
	tests := []struct {
		name     string
		question string
		prev     string
		want     intent
	}{
		// --- data: record type words / plurals ---
		{"lead singular", "Tell me about the lead", "", intentData},
		{"leads plural", "Show me the leads", "", intentData},
		{"prospect singular", "What's the prospect worth", "", intentData},
		{"prospects plural", "List all prospects", "", intentData},
		{"customer singular", "Who owns this customer", "", intentData},
		{"customers plural", "Find my customers", "", intentData},

		// --- data: record number ---
		{"record number", "What's the status of LEAD-000012?", "", intentData},
		{"record number no other cue", "LEAD-000012", "", intentData},

		// --- data: my/our + data noun ---
		{"my leads", "my leads", "", intentData},
		{"our customers", "our customers this week", "", intentData},
		{"my city field", "what's my city", "", intentData},

		// --- data: field words ---
		{"city field", "What city is Acme in?", "", intentData},
		{"phone field", "What's the phone number on file?", "", intentData},
		{"email field", "What email is on record?", "", intentData},
		{"address field", "What's the address?", "", intentData},
		{"status field", "What's the status?", "", intentData},
		{"owner field", "who is the owner", "", intentData},
		{"company field", "what's the company name", "", intentData},
		{"created field", "when was it created", "", intentData},
		{"value field", "what's the value", "", intentData},
		{"total field", "what's the total", "", intentData},

		// --- data: interrogatives ---
		{"who", "Who is assigned to this lead?", "", intentData},
		{"which", "Which customer is this?", "", intentData},
		{"list", "List all customers in the West region", "", intentData},
		{"find", "Find Acme Corp", "", intentData},
		{"show me", "show me the customer", "", intentData},

		// --- help: how do/can I ---
		{"how do I", "How do I archive an old workflow?", "", intentHelp},
		{"how can I", "How can I change my password?", "", intentHelp},
		{"how to", "How to set up a workflow", "", intentHelp},
		{"where do", "Where do I go for settings?", "", intentHelp},
		{"where can", "Where can I add a teammate?", "", intentHelp},
		{"where is", "Where is the settings page?", "", intentHelp},
		{"what is app concept", "What is a workflow?", "", intentHelp},
		{"what does", "What does this button do?", "", intentHelp},
		{"can I", "Can I undo a delete?", "", intentHelp},
		{"steps", "What are the steps to onboard a new user?", "", intentHelp},
		{"set up", "How do I set up SSO?", "", intentHelp},
		{"configure", "How do I configure notifications?", "", intentHelp},
		{"enable", "How do I enable two-factor auth?", "", intentHelp},
		{"turn on", "How do I turn on dark mode?", "", intentHelp},
		{"invite", "How do I invite a teammate?", "", intentHelp},
		{"create a", "How do I create a workflow?", "", intentHelp},
		{"import", "How do I import a spreadsheet?", "", intentHelp},
		{"export", "How do I export a report?", "", intentHelp},
		{"print", "How do I print a document?", "", intentHelp},
		// "email" is also a data field word, so this legitimately overlaps
		// both cue lists — mixed is correct, not a bug.
		{"email a", "How do I email a teammate?", "", intentMixed},

		// --- mixed: both cues present ---
		{"both cues", "What is the status of prospect XYZ?", "", intentMixed},
		{"how do I + record type", "How do I convert this lead?", "", intentMixed},

		// --- mixed: neither cue, no history ---
		{"greeting", "hello there", "", intentMixed},
		{"vague", "tell me more", "", intentMixed},

		// --- follow-up: inherits previous turn when current has no cue ---
		{"follow-up inherits data", "and its city?", "What's LEAD-000012's phone number?", intentData},
		{"follow-up inherits help", "and after that?", "How do I invite a teammate?", intentHelp},
		{"follow-up but has own cue", "what about the customer", "How do I invite a teammate?", intentData},
		{"follow-up neither has cue", "okay thanks", "hello there", intentMixed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyIntent(tt.question, tt.prev); got != tt.want {
				t.Errorf("classifyIntent(%q, %q) = %q, want %q", tt.question, tt.prev, got, tt.want)
			}
		})
	}
}
