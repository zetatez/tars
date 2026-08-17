package secret

import (
	"regexp"
)

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(access[_-]?key|secret[_-]?key|api[_-]?key)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)(password|passwd|token)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)authorization:\s*bearer\s+\S+`),
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),
}

func ContainsSecret(s string) bool {
	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

func Redact(s string) string {
	out := s
	for _, p := range patterns {
		out = p.ReplaceAllString(out, "****")
	}
	return out
}
