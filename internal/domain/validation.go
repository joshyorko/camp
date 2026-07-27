package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var (
	semanticVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	portableIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	lowerSHA256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func decodeExactJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validPortableID(value string) bool {
	if !portableIDPattern.MatchString(value) {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		switch part {
		case "secret", "token", "password", "credential", "port", "timestamp", "session":
			return false
		}
	}
	return true
}

func validLowerSHA256(value string) bool {
	return lowerSHA256Pattern.MatchString(value)
}
