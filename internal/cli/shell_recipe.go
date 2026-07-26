package cli

import "strings"

// shellQuoteArgument renders one literal argument for a copyable POSIX shell
// recipe. Simple arguments stay readable; every other value is single-quoted.
func shellQuoteArgument(value string) string {
	if value != "" && strings.IndexFunc(value, func(char rune) bool {
		return !(char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			strings.ContainsRune("_@%+=:,./-", char))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
