package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDurableJSONDocumentsRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
	parent := GenerationRef{Generation: 41, ArchiveSHA256: strings.Repeat("a", 64)}
	current := GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("b", 64)}
	tools := ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"}
	materialization := Materialization{
		SchemaVersion:    SchemaVersion,
		CanonicalPath:    "/home/josh/.local/share/camp/work/second-brain",
		OriginalPath:     "/home/josh/SecondBrain",
		OwnershipMarker:  "camp:session-123",
		Mode:             MaterializationCreated,
		Device:           2049,
		Inode:            998877,
		CleanupPermitted: true,
	}

	tests := []struct {
		name string
		in   any
		out  any
	}{
		{
			name: "latest pointer",
			in: LatestPointer{
				SchemaVersion: SchemaVersion,
				Capsule:       "second-brain", Lineage: Lineage{Branch: "main"},
				Generation: current, Parent: &parent,
				ObjectKey: "second-brain/generations/42-" + strings.Repeat("b", 64) + ".tar.zst",
				Size:      1048576, CreatedAt: now, Tools: tools, SessionID: "session-123",
			},
			out: &LatestPointer{},
		},
		{
			name: "immutable generation metadata sidecar",
			in: GenerationMetadata{
				SchemaVersion: SchemaVersion,
				Capsule:       "second-brain", Lineage: Lineage{Branch: "main"},
				Generation: current, Parent: &parent,
				ObjectKey:   "second-brain/generations/42-" + strings.Repeat("b", 64) + ".tar.zst",
				MetadataKey: "second-brain/generations/42-" + strings.Repeat("b", 64) + ".json",
				Size:        1048576, CreatedAt: now, Tools: tools, SessionID: "session-123",
				Verified: Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
			},
			out: &GenerationMetadata{},
		},
		{
			name: "lineage writer lease",
			in: WriterLease{
				SchemaVersion: SchemaVersion, Capsule: "second-brain",
				Lineage: Lineage{Branch: "feature-safe"}, SessionID: "session-123",
				Machine: "bluefin", OpenedGeneration: &parent,
				CreatedAt: now, HeartbeatAt: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Minute),
			},
			out: &WriterLease{},
		},
		{
			name: "journal snapshot",
			in: JournalSnapshot{
				SchemaVersion: SchemaVersion, SessionID: "session-123", Capsule: "second-brain",
				Lineage: Lineage{Branch: "main"}, Mode: SessionReadWrite,
				OpenedGeneration: &parent, CurrentBase: &current, ExpectedPointerRevision: "etag-42",
				State: SessionOpen, Materialization: materialization,
				Checkpoint: Checkpoint{State: CheckpointPublished, Generation: &current, PublicationSucceeded: true},
				Cleanup:    Cleanup{State: CleanupPending}, CreatedAt: now, UpdatedAt: now.Add(time.Minute),
			},
			out: &JournalSnapshot{},
		},
		{
			name: "image inventory",
			in: ImageInventory{
				SchemaVersion: SchemaVersion, GeneratedAt: now,
				Images: []Image{{
					EngineImageID:          "sha256:" + strings.Repeat("c", 64),
					OriginalTags:           []string{"localhost:5000/team/app:dev", "team/app:latest"},
					OriginalRepoDigests:    []string{"team/app@sha256:" + strings.Repeat("d", 64)},
					CapturedReference:      "camp.local/session-123/image-1:captured",
					CapturedManifestDigest: "sha256:" + strings.Repeat("e", 64),
					Platform:               Platform{OS: "linux", Architecture: "amd64"},
					Source:                 ImageSourceDaemon, CreatedAt: now,
				}},
			},
			out: &ImageInventory{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"schemaVersion":1`) {
				t.Fatalf("document lacks schemaVersion 1: %s", encoded)
			}
			if err := json.Unmarshal(encoded, tt.out); err != nil {
				t.Fatal(err)
			}
			want := reflect.ValueOf(tt.in)
			got := reflect.ValueOf(tt.out).Elem()
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got.Interface(), want.Interface())
			}
		})
	}
}

func TestWorkspaceMirrorValueHasTruthfulRequiredSerializationTag(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(WorkspaceRecord{}).FieldByName("Mirror")
	if !ok {
		t.Fatal("WorkspaceRecord.Mirror is missing")
	}
	if got := field.Tag.Get("json"); got != "mirror" {
		t.Fatalf("Mirror JSON tag = %q, want required value field", got)
	}
	encoded, err := json.Marshal(WorkspaceRecord{})
	if err != nil || !strings.Contains(string(encoded), `"mirror":{}`) {
		t.Fatalf("empty workspace JSON = %s, %v", encoded, err)
	}
}

func TestDurableYAMLDocumentsRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   any
		out  any
	}{
		{
			name: "capsule metadata",
			in:   CapsuleMetadata{SchemaVersion: SchemaVersion, ID: "second-brain", DefaultBranch: "main", CreatedAt: now},
			out:  &CapsuleMetadata{},
		},
		{
			name: "capsule compatibility lock",
			in: CapsuleLock{
				SchemaVersion: SchemaVersion,
				Room:          RoomLock{Repository: "joshyorko/room-of-requirement", Version: "v1.18.0", Commit: "0aabf18ad291c590498bd8e904a7d09f66378b85", Image: "ghcr.io/joshyorko/room-of-requirement:wolfi", Digest: "sha256:" + strings.Repeat("f", 64)},
				Tools:         ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"},
			},
			out: &CapsuleLock{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := yaml.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "schemaVersion: 1") {
				t.Fatalf("document lacks schemaVersion 1:\n%s", encoded)
			}
			if err := yaml.Unmarshal(encoded, tt.out); err != nil {
				t.Fatal(err)
			}
			want := reflect.ValueOf(tt.in)
			got := reflect.ValueOf(tt.out).Elem()
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got.Interface(), want.Interface())
			}
		})
	}
}

func TestLineagePathsKeepMainAndBranchLeasesSeparate(t *testing.T) {
	tests := []struct {
		name, capsule  string
		lineage        Lineage
		pointer, lease string
	}{
		{"main", "second-brain", Lineage{Branch: "main"}, "second-brain/latest.json", "second-brain/leases/writer.json"},
		{"branch", "second-brain", Lineage{Branch: "feature-safe"}, "second-brain/branches/feature-safe/latest.json", "second-brain/branches/feature-safe/leases/writer.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.lineage.PointerKey(tt.capsule)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.pointer {
				t.Fatalf("pointer key = %q, want %q", got, tt.pointer)
			}
			got, err = tt.lineage.LeaseKey(tt.capsule)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.lease {
				t.Fatalf("lease key = %q, want %q", got, tt.lease)
			}
		})
	}
}

func TestLineagePathsRejectUnsafeObjectKeySegments(t *testing.T) {
	tests := []struct {
		name    string
		capsule string
		branch  string
	}{
		{name: "empty capsule", capsule: "", branch: "main"},
		{name: "empty branch", capsule: "second-brain", branch: ""},
		{name: "capsule dot reserved", capsule: ".", branch: "main"},
		{name: "capsule dot-dot reserved", capsule: "..", branch: "main"},
		{name: "branch dot reserved", capsule: "second-brain", branch: "."},
		{name: "branch dot-dot traversal", capsule: "second-brain", branch: ".."},
		{name: "capsule slash", capsule: "team/capsule", branch: "main"},
		{name: "branch slash", capsule: "second-brain", branch: "feature/unsafe"},
		{name: "capsule backslash", capsule: `team\capsule`, branch: "main"},
		{name: "branch backslash", capsule: "second-brain", branch: `feature\unsafe`},
		{name: "capsule NUL", capsule: "second\x00brain", branch: "main"},
		{name: "branch NUL", capsule: "second-brain", branch: "feature\x00unsafe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lineage := Lineage{Branch: tt.branch}
			if key, err := lineage.PointerKey(tt.capsule); err == nil {
				t.Fatalf("PointerKey accepted unsafe components: %q", key)
			}
			if key, err := lineage.LeaseKey(tt.capsule); err == nil {
				t.Fatalf("LeaseKey accepted unsafe components: %q", key)
			}
		})
	}
}

func TestUnsafeBranchCanNeverCollapseOntoMainKeys(t *testing.T) {
	main := Lineage{Branch: "main"}
	mainPointer, err := main.PointerKey("second-brain")
	if err != nil {
		t.Fatal(err)
	}
	mainLease, err := main.LeaseKey("second-brain")
	if err != nil {
		t.Fatal(err)
	}

	unsafe := Lineage{Branch: ".."}
	if key, err := unsafe.PointerKey("second-brain"); err == nil || key == mainPointer {
		t.Fatalf("unsafe pointer key = %q, error = %v; must reject without colliding with %q", key, err, mainPointer)
	}
	if key, err := unsafe.LeaseKey("second-brain"); err == nil || key == mainLease {
		t.Fatalf("unsafe lease key = %q, error = %v; must reject without colliding with %q", key, err, mainLease)
	}
}
