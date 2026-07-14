package domain

import "path"

type Lineage struct {
	Branch string `json:"branch" yaml:"branch"`
}

func (l Lineage) IsMain() bool {
	return l.Branch == "" || l.Branch == "main"
}

func (l Lineage) PointerKey(capsule string) string {
	if l.IsMain() {
		return path.Join(capsule, "latest.json")
	}
	return path.Join(capsule, "branches", l.Branch, "latest.json")
}

func (l Lineage) LeaseKey(capsule string) string {
	if l.IsMain() {
		return path.Join(capsule, "leases", "writer.json")
	}
	return path.Join(capsule, "branches", l.Branch, "leases", "writer.json")
}
