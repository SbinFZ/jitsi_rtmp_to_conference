// Log redaction: URLs routinely carry one-time signatures and tokens.

package input

import "regexp"

var secretQuery = regexp.MustCompile(`(?i)([?&](?:sign|sig|token|key|password|pwd|auth|jwt|secret)=)[^&\s"']+`)

var userInfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+:[^/@\s]+@`)

// Redact hides credential query parameters and user:password@ in s.
func Redact(s string) string {
	return userInfo.ReplaceAllString(secretQuery.ReplaceAllString(s, `${1}[REDACTED]`), `${1}[REDACTED]@`)
}
