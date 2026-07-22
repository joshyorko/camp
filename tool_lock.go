package camp

import _ "embed"

// distributionToolLock is embedded from the repository-root authoritative lock.
//
//go:embed tools.lock.yaml
var distributionToolLock []byte

// DistributionToolLock returns an isolated copy of Camp's compiled-in tool lock.
func DistributionToolLock() []byte {
	return append([]byte(nil), distributionToolLock...)
}
