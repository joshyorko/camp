package config

import (
	"encoding/json"
	"net/url"
	"strings"
)

const redacted = "[REDACTED]"

func MarshalRedacted(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactValue(generic, ""), "", "  ")
}

func redactValue(value any, key string) any {
	if sensitiveKey(key) {
		return redacted
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, child := range typed {
			result[childKey] = redactValue(child, childKey)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactValue(child, key)
		}
		return result
	case string:
		return redactURL(typed)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	compact := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	for _, marker := range []string{"password", "secret", "token", "credential", "accesskey", "privatekey"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func redactURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	if parsed.User != nil {
		parsed.User = url.User(redacted)
	}
	query := parsed.Query()
	for key := range query {
		if sensitiveKey(key) {
			query.Set(key, redacted)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
