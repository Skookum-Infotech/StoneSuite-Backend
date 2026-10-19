package recordemail

import (
	"net/mail"
	"strings"
)

// FirstAddress returns the first email address in a comma-separated recipient
// list ("a@x.com, Bob <b@y.com>"), or "" when there is none.
func FirstAddress(list string) string {
	parsed, err := mail.ParseAddressList(list)
	if err != nil || len(parsed) == 0 {
		return strings.TrimSpace(strings.SplitN(list, ",", 2)[0])
	}
	return parsed[0].Address
}

// ReplySubject prefixes subject with "Re: " unless it already starts with one,
// so a long conversation does not grow "Re: Re: Re:".
func ReplySubject(subject string) string {
	s := strings.TrimSpace(subject)
	if len(s) >= 3 && strings.EqualFold(s[:3], "re:") {
		return s
	}
	if s == "" {
		return "Re:"
	}
	return "Re: " + s
}
