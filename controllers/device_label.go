package controllers

import "strings"

// uaToken maps a User-Agent substring to the name shown for it. Order
// matters: the first match wins, so more specific tokens come first (Edge and
// Opera user agents also contain "Chrome"; Chrome's contains "Safari").
type uaToken struct {
	needle string
	name   string
}

var uaBrowsers = []uaToken{
	{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"},
	{"Chrome/", "Chrome"}, {"CriOS/", "Chrome"}, {"Safari/", "Safari"},
}

var uaSystems = []uaToken{
	{"Windows", "Windows"}, {"iPhone", "iPhone"}, {"iPad", "iPad"},
	{"Android", "Android"}, {"Mac OS X", "macOS"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
}

// unknownDevice is the label for a User-Agent nothing matches.
const unknownDevice = "Unknown device"

// firstMatch returns the name of the first token found in ua, or "".
func firstMatch(ua string, tokens []uaToken) string {
	for _, t := range tokens {
		if strings.Contains(ua, t.needle) {
			return t.name
		}
	}
	return ""
}

// deviceLabel turns a User-Agent into a short human label such as
// "Chrome on Windows", shown in the "already signed in" prompt.
func deviceLabel(ua string) string {
	browser, system := firstMatch(ua, uaBrowsers), firstMatch(ua, uaSystems)
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	default:
		return unknownDevice
	}
}
