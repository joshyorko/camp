package remoteworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"golang.org/x/sys/unix"
)

type recordingServicesRuntime struct {
	order      []string
	supervisor domain.ProcessRecord
	services   []domain.ServiceUnitRecord
	err        error
}

func (runtime *recordingServicesRuntime) Verify(context.Context, Request) error {
	runtime.order = append(runtime.order, "verify")
	return nil
}

func (runtime *recordingServicesRuntime) Ensure(context.Context, Request) (domain.ProcessRecord, []domain.ServiceUnitRecord, error) {
	runtime.order = append(runtime.order, "ensure")
	return runtime.supervisor, runtime.services, runtime.err
}

func TestStartServicesVerifiesHydrationBeforeStartingExactUnits(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	worker := validWorkerRecord()
	runtime := &recordingServicesRuntime{supervisor: validSupervisorRecord(), services: validRemoteServiceRecords()}

	receipt, err := startServices(t.Context(), request, worker, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.order) != 2 || runtime.order[0] != "verify" || runtime.order[1] != "ensure" {
		t.Fatalf("operation order = %v", runtime.order)
	}
	if receipt.Status != "ready" || receipt.SessionID != request.SessionID ||
		receipt.Worker.Identity != worker.Identity || receipt.Supervisor.Identity == receipt.Worker.Identity ||
		!completeProcessEvidence(receipt.Supervisor) || len(receipt.Services) != 2 || receipt.Services[0].Name != "registry" ||
		receipt.Services[1].Name != "fileserver" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestStartServicesRejectsIncompleteOrUnconfinedEvidence(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	tests := []struct {
		name   string
		mutate func([]domain.ServiceUnitRecord)
	}{
		{"missing service", func(records []domain.ServiceUnitRecord) { records[1] = domain.ServiceUnitRecord{} }},
		{"wildcard mapping", func(records []domain.ServiceUnitRecord) { records[0].Mapping.HostAddress = "0.0.0.0" }},
		{"missing child identity", func(records []domain.ServiceUnitRecord) { records[0].Child.Identity = domain.ProcessIdentity{} }},
		{"missing helper argv digest", func(records []domain.ServiceUnitRecord) { records[0].Helper.ArgvSHA256 = "" }},
		{"shared network namespace", func(records []domain.ServiceUnitRecord) { records[0].Child.NetNS = records[0].Helper.NetNS }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := validRemoteServiceRecords()
			test.mutate(records)
			runtime := &recordingServicesRuntime{supervisor: validSupervisorRecord(), services: records}
			if _, err := startServices(t.Context(), request, validWorkerRecord(), runtime); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("startServices() error = %v", err)
			}
		})
	}
}

func TestStartServicesRejectsIncompleteSupervisorEvidence(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	runtime := &recordingServicesRuntime{services: validRemoteServiceRecords()}
	if _, err := startServices(t.Context(), request, validWorkerRecord(), runtime); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("startServices() error = %v", err)
	}
}

func validSupervisorRecord() domain.ProcessRecord {
	return domain.ProcessRecord{
		Identity:          domain.ProcessIdentity{PID: 99, BootID: "boot", StartTicks: 90},
		DesiredExecutable: "/workspace/.camp-bootstrap/camp-bootstrap", ObservedExecutable: "/workspace/.camp-bootstrap/camp-bootstrap",
		Argv: []string{"/workspace/.camp-bootstrap/camp-bootstrap", "__remote-service-supervisor"}, ArgvSHA256: "edc09ad8ecb4a2a08599619b362e012bf4c9366c7a80545fcc84b1511e117e18",
		ParentPID: 98, PGID: 90, SID: 90, NetNS: "net:[host]",
	}
}

func validWorkerRecord() domain.ProcessRecord {
	return domain.ProcessRecord{
		Identity:          domain.ProcessIdentity{PID: 98, BootID: "boot", StartTicks: 89},
		DesiredExecutable: "/workspace/.camp-bootstrap/camp-bootstrap", ObservedExecutable: "/workspace/.camp-bootstrap/camp-bootstrap",
		Argv: []string{"/workspace/.camp-bootstrap/camp-bootstrap", "__remote-worker"}, ArgvSHA256: "dc3e36d81c500d7581a4359fbb9a3ae7610c82340045b5efb9563455167c2a9f",
		ParentPID: 1, PGID: 89, SID: 89, NetNS: "net:[host]",
	}
}

func TestServiceActorEvidenceRoundTripsAndRejectsEitherIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actors.json")
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	if err := observeServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ServiceActorEvidence){
		func(value *ServiceActorEvidence) { value.Worker.Identity.StartTicks++ },
		func(value *ServiceActorEvidence) { value.Supervisor.ArgvSHA256 = "mismatch" },
	} {
		expected := actors
		mutate(&expected)
		if err := observeServiceActorEvidence(path, expected); !errors.Is(err, ErrServiceEvidence) {
			t.Fatalf("observeServiceActorEvidence() error = %v", err)
		}
	}
}

func TestServiceActorEvidenceRetryConfirmsParentDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actors.json")
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	ops := defaultServiceActorPublicationOps()
	fsyncCalls := 0
	ops.fsync = func(fd int) error {
		fsyncCalls++
		if fsyncCalls == 1 {
			return errors.New("injected post-rename fsync failure")
		}
		return unix.Fsync(fd)
	}
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("first publication error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("renamed evidence missing after durability failure: %v", err)
	}

	retryFsyncCalls := 0
	ops = defaultServiceActorPublicationOps()
	ops.fsync = func(fd int) error {
		retryFsyncCalls++
		return unix.Fsync(fd)
	}
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); err != nil {
		t.Fatalf("retry publication: %v", err)
	}
	if retryFsyncCalls != 1 {
		t.Fatalf("retry parent fsync calls = %d, want 1", retryFsyncCalls)
	}
}

func TestServiceActorEvidenceExistingConfirmationRejectsExactByteInodeReplacementAcrossFsync(t *testing.T) {
	for _, test := range []struct {
		name          string
		replaceBefore bool
	}{
		{"between first observation and parent fsync", true},
		{"after parent fsync before second observation", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "actors.json")
			actors := validServiceActorEvidence()
			if err := publishServiceActorEvidence(path, actors); err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(root, "replacement.json")
			if err := os.WriteFile(replacement, serviceActorEvidenceBody(actors), 0o600); err != nil {
				t.Fatal(err)
			}
			var replacementStat unix.Stat_t
			if err := unix.Lstat(replacement, &replacementStat); err != nil {
				t.Fatal(err)
			}

			ops := defaultServiceActorPublicationOps()
			ops.fsync = func(parentFD int) error {
				if test.replaceBefore {
					if err := os.Rename(replacement, path); err != nil {
						return err
					}
				}
				if err := unix.Fsync(parentFD); err != nil {
					return err
				}
				if !test.replaceBefore {
					return os.Rename(replacement, path)
				}
				return nil
			}
			if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("replacement confirmation error = %v", err)
			}
			var finalStat unix.Stat_t
			if err := unix.Lstat(path, &finalStat); err != nil {
				t.Fatal(err)
			}
			if finalStat.Dev != replacementStat.Dev || finalStat.Ino != replacementStat.Ino {
				t.Fatalf("replacement identity was not preserved: final=%d:%d replacement=%d:%d",
					finalStat.Dev, finalStat.Ino, replacementStat.Dev, replacementStat.Ino)
			}
		})
	}
}

func TestServiceActorEvidenceExistingConfirmationRejectsHardlinkedFinal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(root, "actors-alias.json")); err != nil {
		t.Fatal(err)
	}
	if err := publishServiceActorEvidence(path, actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("hardlinked existing confirmation error = %v", err)
	}
	var finalStat unix.Stat_t
	if err := unix.Lstat(path, &finalStat); err != nil {
		t.Fatal(err)
	}
	if finalStat.Nlink != 2 {
		t.Fatalf("hardlinked final nlink = %d, want preserved 2", finalStat.Nlink)
	}
}

func TestServiceActorEvidenceExistingConfirmationAcceptsExactSingleLinkReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actors.json")
	actors := validServiceActorEvidence()
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatalf("exact single-link replay: %v", err)
	}
}

func TestServiceActorEvidenceUnnamedStagingFailuresLeaveNoDirectoryEntries(t *testing.T) {
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	for _, test := range []struct {
		name   string
		inject func(*serviceActorPublicationOps)
	}{
		{
			name: "write",
			inject: func(ops *serviceActorPublicationOps) {
				ops.write = func(*os.File, []byte) (int, error) { return 0, errors.New("injected write failure") }
			},
		},
		{
			name: "chmod",
			inject: func(ops *serviceActorPublicationOps) {
				ops.chmod = func(*os.File, os.FileMode) error { return errors.New("injected chmod failure") }
			},
		},
		{
			name: "file fsync",
			inject: func(ops *serviceActorPublicationOps) {
				ops.fileSync = func(*os.File) error { return errors.New("injected file fsync failure") }
			},
		},
		{
			name: "pre-link",
			inject: func(ops *serviceActorPublicationOps) {
				ops.beforeLink = func(int, int, string) error { return errors.New("injected pre-link failure") }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "actors.json")
			ops := defaultServiceActorPublicationOps()
			test.inject(&ops)

			err := publishServiceActorEvidenceWithOps(path, actors, ops)
			if !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("publication error = %v", err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("staging failure left directory entries %v: %v", entries, readErr)
			}
		})
	}
}

func TestServiceActorEvidenceFailsClosedWithoutUnnamedOrExactFDPublication(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	for _, test := range []struct {
		name   string
		inject func(*serviceActorPublicationOps)
	}{
		{"O_TMPFILE unsupported", func(ops *serviceActorPublicationOps) {
			ops.openTmpfile = func(int) (int, error) { return -1, unix.EOPNOTSUPP }
		}},
		{"direct link denied and procfs unavailable", func(ops *serviceActorPublicationOps) {
			ops.linkDirect = func(int, int, string) error { return unix.EPERM }
			ops.linkProc = func(int, int, string) error { return unix.ENOENT }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultServiceActorPublicationOps()
			test.inject(&ops)
			if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("publication error = %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed publication left directory entries %v: %v", entries, err)
			}
		})
	}
}

func TestServiceActorEvidenceEEXISTRaceAcceptsOnlyExactStableFinal(t *testing.T) {
	for _, test := range []struct {
		name    string
		content func(ServiceActorEvidence) []byte
		wantOK  bool
	}{
		{"exact", serviceActorEvidenceBody, true},
		{"different", func(ServiceActorEvidence) []byte { return []byte("different\n") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "actors.json")
			actors := validServiceActorEvidence()
			content := test.content(actors)
			ops := defaultServiceActorPublicationOps()
			ops.beforeLink = func(_ int, _ int, name string) error {
				return os.WriteFile(filepath.Join(root, name), content, 0o600)
			}
			err := publishServiceActorEvidenceWithOps(path, actors, ops)
			if test.wantOK && err != nil {
				t.Fatalf("publication error = %v", err)
			}
			if !test.wantOK && !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("publication error = %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(got, content) {
				t.Fatalf("racing final = %q, %v; want preserved %q", got, readErr, content)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 1 || entries[0].Name() != "actors.json" {
				t.Fatalf("directory entries = %v, %v", entries, readErr)
			}
		})
	}
}

func TestServiceActorEvidenceLinkFsyncUnknownOutcomeRetriesOneCanonicalFinal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	ops := defaultServiceActorPublicationOps()
	ops.fsync = func(int) error { return errors.New("injected parent fsync failure") }
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("first publication error = %v", err)
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatalf("retry publication: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "actors.json" {
		t.Fatalf("directory entries = %v, %v", entries, err)
	}
}

func TestServiceActorEvidencePreservesSubstitutionAfterUnknownOutcome(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	ops := defaultServiceActorPublicationOps()
	ops.fsync = func(int) error { return errors.New("injected parent fsync failure") }
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("first publication error = %v", err)
	}
	replacement := []byte("replacement\n")
	if err := os.WriteFile(path+".replacement", replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	if err := publishServiceActorEvidence(path, actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("retry publication error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement = %q, %v; want preserved %q", got, err, replacement)
	}
}

func TestServiceActorEvidencePreservesSubstitutionAfterLinkBeforeIdentityCheck(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	replacement := []byte("replacement\n")
	ops := defaultServiceActorPublicationOps()
	ops.afterLink = func(parentFD int, _ int, name string) error {
		if err := unix.Renameat(parentFD, name, parentFD, name+".linked"); err != nil {
			return err
		}
		return os.WriteFile(path, replacement, 0o600)
	}
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("publication error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement = %q, %v; want preserved %q", got, err, replacement)
	}
}

func TestServiceActorEvidencePublishesExactUnnamedInode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := validServiceActorEvidence()
	var before, stagedAfter, finalAfter unix.Stat_t
	ops := defaultServiceActorPublicationOps()
	ops.beforeLink = func(_ int, stagingFD int, _ string) error {
		return unix.Fstat(stagingFD, &before)
	}
	ops.afterLink = func(parentFD int, stagingFD int, name string) error {
		if err := unix.Fstat(stagingFD, &stagedAfter); err != nil {
			return err
		}
		return unix.Fstatat(parentFD, name, &finalAfter, unix.AT_SYMLINK_NOFOLLOW)
	}
	if err := publishServiceActorEvidenceWithOps(path, actors, ops); err != nil {
		t.Fatal(err)
	}
	if before.Nlink != 0 {
		t.Fatalf("unnamed staging nlink before publication = %d, want 0", before.Nlink)
	}
	if stagedAfter.Dev != finalAfter.Dev || stagedAfter.Ino != finalAfter.Ino ||
		stagedAfter.Nlink != 1 || finalAfter.Nlink != 1 {
		t.Fatalf("published identities staging=%#v final=%#v", stagedAfter, finalAfter)
	}
	if stagedAfter.Mode&0o777 != 0o600 || stagedAfter.Size != int64(len(serviceActorEvidenceBody(actors))) {
		t.Fatalf("published staging shape mode=%#o size=%d", stagedAfter.Mode&0o777, stagedAfter.Size)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, serviceActorEvidenceBody(actors)) {
		t.Fatalf("published bytes = %q, %v", got, err)
	}
}

func validServiceActorEvidence() ServiceActorEvidence {
	return ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
}

func serviceActorEvidenceBody(actors ServiceActorEvidence) []byte {
	body, err := json.Marshal(actors)
	if err != nil {
		panic(err)
	}
	return append(body, '\n')
}

func TestServiceActorEvidenceRejectsSymlinkedFileAndParent(t *testing.T) {
	root := t.TempDir()
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	target := filepath.Join(root, "target.json")
	if err := publishServiceActorEvidence(target, actors); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "actors.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := observeServiceActorEvidence(link, actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("symlinked actor observation error = %v", err)
	}
	if err := publishServiceActorEvidence(link, actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("symlinked actor publication error = %v", err)
	}

	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	parentTarget := filepath.Join(realParent, "actors.json")
	if err := publishServiceActorEvidence(parentTarget, actors); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(realParent, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := publishServiceActorEvidence(filepath.Join(parentLink, "new.json"), actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("symlinked parent publication error = %v", err)
	}
	if err := observeServiceActorEvidence(filepath.Join(parentLink, filepath.Base(parentTarget)), actors); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("symlinked parent observation error = %v", err)
	}
}

func TestServiceActorEvidenceRejectsNonRegularAndExcessiveExistingFiles(t *testing.T) {
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{"directory", func(path string) error { return os.Mkdir(path, 0o700) }},
		{"excessive", func(path string) error {
			return os.WriteFile(path, make([]byte, maxDiagnosticBytes+1), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "actors.json")
			if err := test.setup(path); err != nil {
				t.Fatal(err)
			}
			if err := observeServiceActorEvidence(path, actors); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("observeServiceActorEvidence() error = %v", err)
			}
			if err := publishServiceActorEvidence(path, actors); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("publishServiceActorEvidence() error = %v", err)
			}
		})
	}
}

func TestServiceActorObserverRejectsNamedFileReplacementDuringRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "actors.json")
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement.json")
	if err := os.WriteFile(replacement, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeServiceActorFile(path, func() {
		if renameErr := os.Rename(replacement, path); renameErr != nil {
			t.Errorf("replace actor evidence: %v", renameErr)
		}
	}); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("observeServiceActorFile() error = %v", err)
	}
}

func TestServiceActorEvidenceRejectsConflatedOrWrongRoleCommands(t *testing.T) {
	for _, mutate := range []func(*ServiceActorEvidence){
		func(value *ServiceActorEvidence) { value.Supervisor.Identity = value.Worker.Identity },
		func(value *ServiceActorEvidence) { value.Worker.Argv[1] = "__remote-service-supervisor" },
		func(value *ServiceActorEvidence) { value.Supervisor.Argv[1] = "__remote-worker" },
	} {
		actors := ServiceActorEvidence{
			SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
			Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
		}
		mutate(&actors)
		if err := publishServiceActorEvidence(filepath.Join(t.TempDir(), "actors.json"), actors); !errors.Is(err, ErrServiceEvidence) {
			t.Fatalf("publishServiceActorEvidence() error = %v", err)
		}
	}
}

func validRemoteServiceRecords() []domain.ServiceUnitRecord {
	record := func(name string, hostPort, guestPort, helperPID, childPID int) domain.ServiceUnitRecord {
		return domain.ServiceUnitRecord{
			Name: name, LaunchToken: "session-" + name + "-v1",
			Confinement: domain.ConfinementRecord{
				Executable: "/workspace/.camp/runtime/pasta", Version: "pasta 1",
				EnvironmentFingerprint: "fingerprint", Boundary: "remote-workspace",
			},
			Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: hostPort, GuestPort: guestPort},
			Helper: domain.ProcessRecord{
				Identity:          domain.ProcessIdentity{PID: helperPID, BootID: "boot", StartTicks: 100},
				DesiredExecutable: "/workspace/.camp/runtime/pasta", ObservedExecutable: "/workspace/.camp/runtime/pasta",
				Argv: []string{"/workspace/.camp/runtime/pasta", "--foreground"}, ArgvSHA256: "helper-digest",
				PGID: helperPID, SID: helperPID, NetNS: "net:[host]",
			},
			Child: domain.ProcessRecord{
				Identity:          domain.ProcessIdentity{PID: childPID, BootID: "boot", StartTicks: 101},
				DesiredExecutable: "/workspace/.camp/runtime/hauler", ObservedExecutable: "/workspace/.camp/runtime/hauler",
				Argv: []string{"/workspace/.camp/runtime/hauler", "store"}, ArgvSHA256: "child-digest",
				ParentPID: helperPID, PGID: helperPID, SID: helperPID, NetNS: "net:[child]",
			},
			DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
		}
	}
	return []domain.ServiceUnitRecord{
		record("registry", remoteRegistryPort, remoteRegistryGuestPort, 101, 102),
		record("fileserver", remoteFileserverPort, remoteFileserverGuestPort, 201, 202),
	}
}
