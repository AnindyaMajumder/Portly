// Package textsafe makes process-derived text safe to show.
package textsafe

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const mask = "***"

// Clean removes terminal control, bidi and invisible characters.
func Clean(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r):
			return '?'
		case unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co):
			return -1
		}
		return r
	}, text)
}

const secretWord = `(?:pass(?:word|wd|phrase)?|pwd|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|auth|credentials?|bearer|signature|sig)`

var (
	secretName = regexp.MustCompile(`(?i)^(?:[\w.-]*[-_.])?` + secretWord + `(?:[-_][\w-]*)?$|(?i)^[\w.-]*(?:password|passwd|secret|token|apikey|api_key)[\w-]*$`)
	assignment = regexp.MustCompile(`^(-{0,2}[\w.:-]+)([=:])(.+)$`)
	urlUser    = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s@]*@)+`)
	authHeader = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[\w.~+/=-]{6,}`)
	knownToken = regexp.MustCompile(`\b(?:gh[pousr]_\w{20,}|github_pat_\w{20,}|sk-[\w-]{16,}|xox[abprs]-[\w-]{10,}|AKIA[0-9A-Z]{16}|glpat-[\w-]{16,}|eyJ[\w-]{8,}\.[\w-]{8,}\.[\w-]*)`)
	inlineFlag = regexp.MustCompile(`(^|\s)(--?[\w.-]+)(\s+)("[^"]*"|'[^']*'|[^\s-]\S*)`)
	safeName   = regexp.MustCompile(`(?i)^(?:[\w.-]*[-_.])?(?:tokenizers?|max[-_]?(?:new[-_]?)?tokens|(?:no[-_])?auth(?:or|ors|entic)?)(?:[-_.][\w-]*)?$`)
	queryParam = regexp.MustCompile(`([?&;]|\s)([\w.-]+)=([^&\s;]+)`)
)

// MaskArgs hides secret values. Best effort, patterns can be missed.
func MaskArgs(args []string) []string {
	masked := make([]string, len(args))
	hideNext := false
	for i, arg := range args {
		switch {
		case hideNext && !looksLikeFlag(arg):
			masked[i], hideNext = mask, false
		default:
			masked[i] = maskOne(arg)
			hideNext = i > 0 && isSecretFlag(arg)
		}
	}
	return masked
}

// looksLikeFlag tells a following flag from a secret value.
func looksLikeFlag(arg string) bool {
	return len(arg) > 2 && strings.HasPrefix(arg, "--")
}

// isSecretFlag matches flags whose next argument is a secret.
func isSecretFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.ContainsAny(arg, "=: ") && isSecretName(strings.TrimLeft(arg, "-"))
}

// isSecretName matches names of secrets, minus known harmless ones.
func isSecretName(name string) bool {
	return secretName.MatchString(name) && !safeName.MatchString(name)
}

// maskInline masks a flag value inside one longer argument.
func maskInline(match string) string {
	parts := inlineFlag.FindStringSubmatch(match)
	if isSecretFlag(parts[2]) {
		return parts[1] + parts[2] + parts[3] + mask
	}
	return match
}

// maskOne applies every pattern to a single argument.
func maskOne(arg string) string {
	// Shell snippets such as sh -c "app --token x"
	arg = inlineFlag.ReplaceAllStringFunc(arg, maskInline)
	arg = authHeader.ReplaceAllString(arg, "$1 "+mask)
	arg = knownToken.ReplaceAllString(arg, mask)
	arg = urlUser.ReplaceAllStringFunc(arg, maskURLUser)
	arg = queryParam.ReplaceAllStringFunc(arg, maskParam)
	parts := assignment.FindStringSubmatch(arg)
	if parts == nil {
		return arg
	}
	name := strings.TrimLeft(parts[1], "-")
	// JVM system properties: -Ddb.password=x
	name = strings.TrimPrefix(name, "D")
	if isSecretName(name) && !strings.HasPrefix(parts[3], "//") {
		return parts[1] + parts[2] + mask
	}
	return arg
}

// maskParam masks one query or key=value pair.
func maskParam(match string) string {
	parts := queryParam.FindStringSubmatch(match)
	if isSecretName(parts[2]) {
		return parts[1] + parts[2] + "=" + mask
	}
	return match
}

// maskURLUser masks passwords and tokens in a URL.
func maskURLUser(match string) string {
	scheme, userinfo, _ := strings.Cut(match, "://")
	user, _, hasPassword := strings.Cut(strings.TrimSuffix(userinfo, "@"), ":")
	if !hasPassword {
		// A lone user part is often a token
		return scheme + "://" + mask + "@"
	}
	if decoded, err := url.PathUnescape(user); err != nil || decoded == "" {
		user = ""
	}
	return scheme + "://" + user + ":" + mask + "@"
}

// CommandLine joins masked, cleaned arguments with shell-like quoting.
func CommandLine(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range MaskArgs(args) {
		arg = Clean(arg)
		if arg == "" || strings.ContainsAny(arg, " \t\"'\\$`;&|<>(){}*?!#~") && arg != mask {
			arg = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		quoted = append(quoted, arg)
	}
	return strings.Join(quoted, " ")
}
