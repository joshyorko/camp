package domain

import (
	"fmt"
	"strings"
)

type Lineage struct {
	Branch string `json:"branch" yaml:"branch"`
}

func (l Lineage) IsMain() bool {
	return l.Branch == "main"
}

func (l Lineage) PointerKey(capsule string) (string, error) {
	if err := validateObjectKeySegment("capsule", capsule); err != nil {
		return "", err
	}
	if err := validateObjectKeySegment("branch", l.Branch); err != nil {
		return "", err
	}
	if l.IsMain() {
		return capsule + "/latest.json", nil
	}
	return capsule + "/branches/" + l.Branch + "/latest.json", nil
}

func (l Lineage) LeaseKey(capsule string) (string, error) {
	if err := validateObjectKeySegment("capsule", capsule); err != nil {
		return "", err
	}
	if err := validateObjectKeySegment("branch", l.Branch); err != nil {
		return "", err
	}
	if l.IsMain() {
		return capsule + "/leases/writer.json", nil
	}
	return capsule + "/branches/" + l.Branch + "/leases/writer.json", nil
}

func validateObjectKeySegment(name, value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("invalid %s object-key segment %q", name, value)
	}
	return nil
}
