package common

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SafeErrorMaxRunes = 512

var (
	sensitiveErrorHeaderPattern = regexp.MustCompile(`(?im)\b(authorization|proxy-authorization|cookie|set-cookie)\s*[:=]\s*[^\r\n]*`)
	sensitiveErrorBearerPattern = regexp.MustCompile(`(?i)\bbearer[ \t]+[^\s,;]+`)
	sensitiveErrorLabelPattern  = regexp.MustCompile(`(?i)\b(authorization|proxy[-_ ]?authorization|access[-_ ]?token|refresh[-_ ]?token|api[-_ ]?key|token|password|passwd|pwd|cookie|secret)["']?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
)

func SanitizeErrorMessage(message string, secrets ...string) string {
	message = strings.ToValidUTF8(message, "�")

	knownSecrets := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		secret = strings.ToValidUTF8(secret, "")
		if secret != "" {
			knownSecrets = append(knownSecrets, secret)
		}
	}
	sort.Slice(knownSecrets, func(i, j int) bool {
		return len(knownSecrets[i]) > len(knownSecrets[j])
	})
	for _, secret := range knownSecrets {
		message = strings.ReplaceAll(message, secret, "***")
	}

	message = sensitiveErrorHeaderPattern.ReplaceAllString(message, "$1: ***")
	message = sensitiveErrorBearerPattern.ReplaceAllString(message, "Bearer ***")
	message = sensitiveErrorLabelPattern.ReplaceAllString(message, "$1=***")
	message = MaskSensitiveInfo(message)
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")

	if utf8.RuneCountInString(message) <= SafeErrorMaxRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:SafeErrorMaxRunes])
}
