package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestDurableConfigurationExcludesHostCredentialsAndRetainsRecoveryInputs(t *testing.T) {
	t.Parallel()
	paths, err := ResolveXDGPaths(XDGInput{Home: "/home/josh", Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := ResolveFileBackend("file:///mnt/camp-backend")
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{
		Bootstrap: Bootstrap{
			Capsule: "brain", Backend: "file:///mnt/camp-backend", Source: "/home/josh/SecondBrain",
			RegistryPort: 45001, FileserverPort: 45002, AccessToken: "super-secret-access-token",
		},
		DevcontainerPath: "/home/josh/SecondBrain/.devcontainer/devcontainer.json",
	}
	record := DurableConfiguration(runtime, backend, paths)
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), runtime.AccessToken) || strings.Contains(strings.ToLower(string(body)), "accesstoken") {
		t.Fatalf("durable configuration leaked host credential: %s", body)
	}
	if record.Capsule != runtime.Capsule || record.BackendKind != "file" || record.BackendURL != backend.SanitizedURL || record.BackendFingerprint != backend.Fingerprint || record.Paths != (domain.SessionPaths{
		DataRoot: paths.DataRoot, WorkRoot: paths.WorkRoot, StoreRoot: paths.StoreRoot, SessionRoot: paths.SessionRoot, CacheRoot: paths.CacheRoot,
	}) {
		t.Fatalf("durable configuration = %#v", record)
	}
}

func TestDurableBackendConfigurationPersistsOnlySanitizedS3Identity(t *testing.T) {
	backend, err := ResolveBackend("s3://camp-bucket/team", S3Values{
		Endpoint: "https://s3.example.test", Region: "us-east-1", PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := DurableBackendConfiguration(Runtime{Bootstrap: Bootstrap{Capsule: "brain"}}, backend, XDGPaths{})
	if record.BackendKind != "s3" || record.BackendURL != "s3://camp-bucket/team" || record.BackendFingerprint != backend.Fingerprint {
		t.Fatalf("durable S3 identity = %#v", record)
	}
}
