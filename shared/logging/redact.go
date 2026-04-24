package logging

import "strings"

// minRedactLength is the minimum number of bytes a secret value must have
// before it is eligible for redaction. Values shorter than this are skipped
// to avoid false-positive redaction of common short strings (e.g. "true",
// "yes", "en") that happen to collide with a configured secret.
const minRedactLength = 6

// Redact replaces every occurrence of each secret value found in text with
// "***". Only values whose byte length is >= minRedactLength are considered;
// shorter values are silently skipped. An empty or nil secrets map returns
// text unchanged.
func Redact(text string, secrets map[string]string) string {
	if len(secrets) == 0 {
		return text
	}
	for _, v := range secrets {
		if len(v) < minRedactLength {
			continue
		}
		text = strings.ReplaceAll(text, v, "***")
	}
	return text
}
