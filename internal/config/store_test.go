package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreUpdatePersistsOnlyAllowedNonSecretFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "camp", "config.yaml")
	store := NewStore(path)
	want := Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp", Source: "/srv/brain", DevPodProvider: "docker", RegistryPort: 5001, FileserverPort: 8081}
	if err := store.Update(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	wantMachine := want
	wantMachine.DefaultCapsule = ""
	wantMachine.Source = ""
	if got != wantMachine {
		t.Fatalf("Read() = %#v, want machine defaults %#v", got, wantMachine)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "token") || strings.Contains(strings.ToLower(string(body)), "credential") {
		t.Fatalf("persisted secret-shaped field: %s", body)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, error = %v", info.Mode().Perm(), err)
	}
}

func TestStoreRejectsCredentialBearingValuesBeforeReplacingConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewStore(path)
	baseline := Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp"}
	if err := store.Update(baseline); err != nil {
		t.Fatal(err)
	}
	machineBaseline := baseline
	machineBaseline.DefaultCapsule = ""
	for _, candidate := range []Persistent{
		{DefaultCapsule: "brain", Backend: "https://user:secret@example.test/camp"},
		{DefaultCapsule: "brain", Source: "https://example.test/camp?access_token=secret"},
	} {
		if err := store.Update(candidate); !errors.Is(err, ErrCredentialPersistence) {
			t.Fatalf("Update(%#v) error = %v, want ErrCredentialPersistence", candidate, err)
		}
		got, err := store.Read()
		if err != nil || got != machineBaseline {
			t.Fatalf("config changed after rejected update: %#v, %v", got, err)
		}
	}
}

func TestValidatePersistentRejectsInvalidFirstRunValuesWithoutWriting(t *testing.T) {
	t.Parallel()
	for _, value := range []Persistent{
		{DefaultCapsule: "brain", Backend: "https://user:secret@example.test/camp", Source: "/brain", DevPodProvider: "docker"},
		{DefaultCapsule: "brain", Backend: "file:///srv/camp", Source: "/brain", DevPodProvider: "../unsafe"},
	} {
		if err := ValidatePersistent(value); err == nil {
			t.Fatalf("ValidatePersistent(%#v) succeeded", value)
		}
	}
}

func TestStoreModifyPreservesValidS3Configuration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "camp", "config.yaml")
	store := NewStore(path)
	s3 := S3Values{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", PathStyle: true, Insecure: true}
	baseline := Persistent{DefaultCapsule: "old", Backend: "s3://camp-bucket/team", S3: s3, RegistryPort: 5001, FileserverPort: 8081}
	if err := store.Update(baseline); err != nil {
		t.Fatal(err)
	}
	written, err := store.Modify(func(value *Persistent) error {
		value.DefaultCapsule = "brain"
		value.Source = "/srv/brain"
		value.DevPodProvider = "room-of-requirement"
		value.DevPodContext = "ror"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if written.S3 != s3 || written.RegistryPort != 5001 || written.FileserverPort != 8081 || written.DevPodContext != "ror" {
		t.Fatalf("Modify() = %#v, want S3, ports, and context preserved", written)
	}
}

func TestStoreModifySerializesConcurrentReadModifyWrite(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "camp", "config.yaml"))
	if err := store.Update(Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp"}); err != nil {
		t.Fatal(err)
	}
	const writers = 24
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(writers)
	for index := 0; index < writers; index++ {
		go func() {
			defer wait.Done()
			<-start
			if _, err := store.Modify(func(value *Persistent) error {
				value.RegistryPort++
				return nil
			}); err != nil {
				t.Errorf("Modify(): %v", err)
			}
		}()
	}
	close(start)
	wait.Wait()
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.RegistryPort != writers {
		t.Fatalf("RegistryPort = %d, want %d serialized updates", got.RegistryPort, writers)
	}
}

func TestStoreRejectsCredentialBearingS3Endpoint(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "camp", "config.yaml"))
	value := Persistent{DefaultCapsule: "brain", Backend: "s3://camp-bucket/team", S3: S3Values{
		Endpoint: "https://user:secret@s3.example.test", Region: "us-east-1", PathStyle: true,
	}}
	if err := store.Update(value); !errors.Is(err, ErrCredentialPersistence) {
		t.Fatalf("Update() error = %v, want ErrCredentialPersistence", err)
	}
}

func TestStoreWritesMachineDefaultsWithoutLegacyCampSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	value := Persistent{
		DefaultCapsule: "legacy-brain",
		Source:         "/srv/legacy-brain",
		Backend:        "file:///srv/camp",
		DevPodProvider: "docker",
		DevPodContext:  "default",
	}
	if err := NewStore(path).Update(value); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "defaultCapsule") || strings.Contains(string(body), "source:") {
		t.Fatalf("machine config persisted camp selection:\n%s", body)
	}
	if !strings.Contains(string(body), "backend: file:///srv/camp") {
		t.Fatalf("machine config omitted backend default:\n%s", body)
	}
}
