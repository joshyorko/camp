package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemoteDataPlaneRecordRoundTripsWithoutChangingJournalSchema(t *testing.T) {
	record := RemoteDataPlaneRecord{
		Mode: DataPlaneHaulerKitV1, AttemptID: "session-hauler-kit-v1", BootstrapRoot: "/data/session/bootstrap",
		KitSHA256: strings.Repeat("a", 64), KitSize: 42, ManifestSHA256: strings.Repeat("b", 64), ManifestSize: 21,
		SourceImage:   "example.test/room@sha256:" + strings.Repeat("c", 64),
		OuterImage:    "sha256:" + strings.Repeat("e", 64),
		RequestSchema: 1, RequestSession: "session", WorkspaceRoot: "/workspaces/brain",
		RuntimeRoot: "/var/lib/camp/session", ManifestPath: "/var/lib/camp/session/camp-hauler-kit.json",
		Architecture: "linux/amd64", ConfigSHA256: strings.Repeat("d", 64), ConfigSize: 512,
	}
	snapshot := JournalSnapshot{SchemaVersion: SchemaVersion, Recovery: RecoveryRecord{RemoteDataPlane: &record}}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded JournalSnapshot
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != SchemaVersion || decoded.Recovery.RemoteDataPlane == nil || *decoded.Recovery.RemoteDataPlane != record {
		t.Fatalf("decoded remote data plane = schema:%d record:%#v", decoded.SchemaVersion, decoded.Recovery.RemoteDataPlane)
	}
}

func TestLegacyJournalOmitsRemoteDataPlaneRecord(t *testing.T) {
	body, err := json.Marshal(JournalSnapshot{SchemaVersion: SchemaVersion, Recovery: RecoveryRecord{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "remoteDataPlane") {
		t.Fatalf("legacy journal unexpectedly contains remote data plane: %s", body)
	}
}
