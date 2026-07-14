package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestJournalSnapshotRoundTripsCompleteFreshProcessRecoveryContract(t *testing.T) {
	t.Parallel()
	snapshot := JournalSnapshot{
		SchemaVersion: SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: Lineage{Branch: "main"}, State: SessionOpening,
		Recovery: RecoveryRecord{
			Configuration: ConfigurationRecord{
				Capsule: "brain", BackendKind: "file", BackendURL: "file:///mnt/camp", BackendFingerprint: "fingerprint",
				Source: "/home/josh/SecondBrain", RegistryPort: 45001, FileserverPort: 45002,
				Paths: SessionPaths{DataRoot: "/data/camp", WorkRoot: "/data/camp/work", StoreRoot: "/data/camp/stores", SessionRoot: "/data/camp/sessions", CacheRoot: "/cache/camp"},
			},
			Session: SessionArtifactPaths{Root: "/data/camp/sessions/session-a", RuntimeRoot: "/data/camp/sessions/session-a/runtime", HaulPath: "/data/camp/stores/session-a/generation.tar.zst", RegistryOverlay: "/data/camp/sessions/session-a/registry"},
			Source:  SourceDecision{Kind: "adopted", Root: "/home/josh/SecondBrain", Initialized: true},
			DesiredServices: []DesiredServiceRecord{{
				Name: "registry", LaunchToken: "launch-id", Mapping: EndpointMapping{HostAddress: "127.0.0.1", HostPort: 45001, GuestPort: 5000},
				PIDPath: "/data/camp/sessions/session-a/registry.pid", LogPath: "/data/camp/sessions/session-a/registry.log",
				Child: CommandRecord{Executable: "/opt/hauler", Argv: []string{"store", "serve", "registry"}, Directory: "/home/josh/SecondBrain"},
			}},
			Entry:      EntryRequestRecord{Mode: "terminal", Target: "MemoryD"},
			Forwarding: []ForwardingRecord{{Name: "registry", LocalEndpoint: "127.0.0.1:45001", WorkspaceEndpoint: "127.0.0.1:45001", DesiredState: RuntimeDesiredRunning}},
			Cleanup:    CleanupPolicy{WorkspaceAction: "delete", RemoveOwnedMaterialization: true, RemoveSessionArtifacts: true},
		},
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded JournalSnapshot
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Recovery, snapshot.Recovery) {
		t.Fatalf("recovery round trip\n got: %#v\nwant: %#v", decoded.Recovery, snapshot.Recovery)
	}
}
