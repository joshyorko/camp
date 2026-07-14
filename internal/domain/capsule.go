package domain

import "time"

const SchemaVersion = 1

type CapsuleMetadata struct {
	SchemaVersion int       `json:"schemaVersion" yaml:"schemaVersion"`
	ID            string    `json:"id" yaml:"id"`
	DefaultBranch string    `json:"defaultBranch" yaml:"defaultBranch"`
	CreatedAt     time.Time `json:"createdAt" yaml:"createdAt"`
}

type CapsuleLock struct {
	SchemaVersion int          `json:"schemaVersion" yaml:"schemaVersion"`
	Room          RoomLock     `json:"room" yaml:"room"`
	Tools         ToolVersions `json:"tools" yaml:"tools"`
}

type RoomLock struct {
	Repository string `json:"repository" yaml:"repository"`
	Version    string `json:"version" yaml:"version"`
	Commit     string `json:"commit" yaml:"commit"`
	Image      string `json:"image" yaml:"image"`
	Digest     string `json:"digest" yaml:"digest"`
}
