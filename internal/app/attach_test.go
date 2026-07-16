package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
)

func TestAttachMapsTargetAndChoosesExactlyOneEntryPath(t *testing.T) {
	t.Parallel()
	snapshot := domain.JournalSnapshot{
		SessionID: "session-1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, State: domain.SessionOpen,
		Materialization: domain.Materialization{CanonicalPath: "/staging/brain", OwnershipMarker: "owned"},
		Workspace:       domain.WorkspaceRecord{ID: "camp-brain", Context: "ctx", Provider: "docker", StagingRoot: "/staging/brain"},
	}
	for _, test := range []struct {
		name    string
		entry   devpodadapter.IDEEntry
		wantSSH bool
	}{
		{name: "terminal", entry: devpodadapter.IDEEntry{IDE: devpodadapter.IDETerminal}, wantSSH: true},
		{name: "insiders", entry: devpodadapter.IDEEntry{IDE: devpodadapter.IDEVSCodeInsiders}},
	} {
		t.Run(test.name, func(t *testing.T) {
			devpod := &attachDevPod{effectiveRoot: "/workspaces/brain"}
			ownership := &attachOwnership{}
			useCase := NewAttach(AttachDependencies{
				Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{snapshot}}, Ownership: ownership,
				Target: attachTargetResolver{result: target.Result{Absolute: "/staging/brain/Memory D", Relative: "Memory D"}}, DevPod: devpod,
			})
			result, err := useCase.Run(context.Background(), AttachRequest{
				Selector: SessionSelector{Capsule: "brain", Branch: "main"}, Target: "Memory D", Entry: test.entry,
				SSH: devpodadapter.SSHOptions{User: "coder", ForwardPorts: []string{"127.0.0.1:3000:127.0.0.1:3000"}},
			})
			if err != nil {
				t.Fatalf("Attach.Run() error = %v", err)
			}
			if result.Session.SessionID != snapshot.SessionID || result.MappedTarget != "/workspaces/brain/Memory D" {
				t.Fatalf("result = %#v", result)
			}
			if ownership.got != snapshot.Materialization {
				t.Fatalf("ownership revalidation = %#v", ownership.got)
			}
			if test.wantSSH {
				want := devpodadapter.SSHOptions{
					WorkspaceID: "camp-brain", Context: "ctx", Workdir: "/workspaces/brain/Memory D", User: "coder",
					ForwardPorts: []string{"127.0.0.1:3000:127.0.0.1:3000"}, StartServices: true,
				}
				if len(devpod.ssh) != 1 || !reflect.DeepEqual(devpod.ssh[0], want) || len(devpod.ide) != 0 {
					t.Fatalf("terminal calls: ssh=%#v ide=%#v", devpod.ssh, devpod.ide)
				}
			} else {
				want := devpodadapter.IDEOpenOptions{IDE: test.entry, WorkspaceID: "camp-brain", ContainerTarget: "/workspaces/brain/Memory D"}
				if len(devpod.ide) != 1 || !reflect.DeepEqual(devpod.ide[0], want) || len(devpod.ssh) != 0 {
					t.Fatalf("IDE calls: ssh=%#v ide=%#v", devpod.ssh, devpod.ide)
				}
			}
		})
	}
}

func TestAttachFailsClosedBeforeEntryForUnsafeSession(t *testing.T) {
	t.Parallel()
	snapshot := domain.JournalSnapshot{SessionID: "opening", State: domain.SessionOpening}
	devpod := &attachDevPod{effectiveRoot: "/workspaces/brain"}
	useCase := NewAttach(AttachDependencies{
		Sessions: fakeSessionLister{sessions: []domain.JournalSnapshot{snapshot}}, Ownership: &attachOwnership{},
		Target: attachTargetResolver{}, DevPod: devpod,
	})
	if _, err := useCase.Run(context.Background(), AttachRequest{Entry: devpodadapter.IDEEntry{IDE: devpodadapter.IDETerminal}}); !errors.Is(err, ErrAttachSessionNotOpen) {
		t.Fatalf("Attach.Run() error = %v, want ErrAttachSessionNotOpen", err)
	}
	if len(devpod.ssh) != 0 || len(devpod.ide) != 0 {
		t.Fatalf("entry effects occurred: %#v", devpod)
	}
}

type attachOwnership struct {
	got domain.Materialization
	err error
}

func (o *attachOwnership) Revalidate(materialization domain.Materialization) error {
	o.got = materialization
	return o.err
}

type attachTargetResolver struct {
	result target.Result
	err    error
}

func (r attachTargetResolver) Resolve(context.Context, string, string) (target.Result, error) {
	return r.result, r.err
}

type attachDevPod struct {
	effectiveRoot string
	ssh           []devpodadapter.SSHOptions
	ide           []devpodadapter.IDEOpenOptions
}

func (d *attachDevPod) ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error) {
	return d.effectiveRoot, nil
}

func (d *attachDevPod) SSH(_ context.Context, options devpodadapter.SSHOptions) (ports.Result, error) {
	d.ssh = append(d.ssh, options)
	return ports.Result{}, nil
}

func (d *attachDevPod) OpenNestedIDE(_ context.Context, options devpodadapter.IDEOpenOptions) (ports.Result, error) {
	d.ide = append(d.ide, options)
	return ports.Result{}, nil
}
