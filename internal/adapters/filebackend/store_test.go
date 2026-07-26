package filebackend_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/ports"
)

type byteSource []byte

func (s byteSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s)), nil
}

type cancelOnCloseSource struct {
	body   []byte
	cancel context.CancelFunc
}

func (s cancelOnCloseSource) Open() (io.ReadCloser, error) {
	return &cancelOnCloseReader{Reader: bytes.NewReader(s.body), cancel: s.cancel}, nil
}

type cancelOnCloseReader struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r *cancelOnCloseReader) Close() error {
	r.cancel()
	return nil
}

type cancelOnErrContext struct {
	calls    int
	cancelOn int
	done     chan struct{}
	canceled bool
}

func (c *cancelOnErrContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelOnErrContext) Done() <-chan struct{}       { return c.done }
func (c *cancelOnErrContext) Value(any) any               { return nil }
func (c *cancelOnErrContext) Err() error {
	c.calls++
	if c.calls >= c.cancelOn {
		if !c.canceled {
			close(c.done)
			c.canceled = true
		}
		return context.Canceled
	}
	return nil
}

func newCancelOnErrContext(cancelOn int) *cancelOnErrContext {
	return &cancelOnErrContext{cancelOn: cancelOn, done: make(chan struct{})}
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func newStore(t *testing.T) (*filebackend.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "objects")
	store, err := filebackend.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func readObject(t *testing.T, store ports.ObjectStore, key string) []byte {
	t.Helper()
	r, _, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestStorePutImmutableAcceptsEqualBytesAndRejectsUnequalBytes(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	key := "capsule/generations/1-archive.tar.zst"
	body := []byte("verified generation bytes")

	first, err := store.PutImmutable(ctx, key, byteSource(body), digest(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutImmutable(ctx, key, byteSource(body), digest(body), int64(len(body)))
	if err != nil {
		t.Fatalf("equal immutable write: %v", err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("equal immutable write changed revision: %q -> %q", first.Revision, second.Revision)
	}
	if first.SHA256 != digest(body) || first.Size != int64(len(body)) {
		t.Fatalf("metadata = %#v", first)
	}

	different := []byte("different generation bytes")
	_, err = store.PutImmutable(ctx, key, byteSource(different), digest(different), int64(len(different)))
	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("unequal immutable write error = %v, want conflict", err)
	}
	if got := readObject(t, store, key); !bytes.Equal(got, body) {
		t.Fatalf("immutable object changed to %q", got)
	}
}

func TestStorePutImmutableRejectsSourceIntegrityMismatch(t *testing.T) {
	store, _ := newStore(t)
	body := []byte("generation")

	_, err := store.PutImmutable(context.Background(), "capsule/generations/g.tar.zst", byteSource(body), strings.Repeat("0", 64), int64(len(body)))
	if !errors.Is(err, ports.ErrIntegrity) {
		t.Fatalf("digest mismatch error = %v, want integrity error", err)
	}
	if _, err := store.Head(context.Background(), "capsule/generations/g.tar.zst"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("mismatched source was published: %v", err)
	}
}

func TestStoreConditionalWritesAndDeletesUseRevisions(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	key := "capsule/latest.json"

	created, err := store.PutConditional(ctx, key, []byte(`{"generation":1}`), ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision == "" {
		t.Fatal("created object has an empty revision")
	}
	if _, err := store.PutConditional(ctx, key, []byte(`{"generation":2}`), ports.WriteCondition{MustBeAbsent: true}); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("second create error = %v, want conflict", err)
	}
	if _, err := store.PutConditional(ctx, key, []byte(`{"generation":2}`), ports.WriteCondition{MatchRevision: "stale"}); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale replace error = %v, want conflict", err)
	}

	updated, err := store.PutConditional(ctx, key, []byte(`{"generation":2}`), ports.WriteCondition{MatchRevision: created.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision == "" || updated.Revision == created.Revision {
		t.Fatalf("replacement did not receive a fresh opaque revision: %q -> %q", created.Revision, updated.Revision)
	}
	if err := store.DeleteConditional(ctx, key, created.Revision); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale delete error = %v, want conflict", err)
	}
	if err := store.DeleteConditional(ctx, key, updated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(ctx, key); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("head after delete error = %v, want not found", err)
	}
}

func TestStorePutConditionalRequiresExactlyOneCondition(t *testing.T) {
	for _, test := range []struct {
		name      string
		existing  bool
		condition func(ports.Revision) ports.WriteCondition
	}{
		{name: "zero condition against absent key", condition: func(ports.Revision) ports.WriteCondition { return ports.WriteCondition{} }},
		{name: "zero condition against existing key", existing: true, condition: func(ports.Revision) ports.WriteCondition { return ports.WriteCondition{} }},
		{name: "both conditions", existing: true, condition: func(revision ports.Revision) ports.WriteCondition {
			return ports.WriteCondition{MustBeAbsent: true, MatchRevision: revision}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newStore(t)
			ctx := context.Background()
			key := "capsule/latest.json"
			var revision ports.Revision
			if test.existing {
				created, err := store.PutConditional(ctx, key, []byte("original"), ports.WriteCondition{MustBeAbsent: true})
				if err != nil {
					t.Fatal(err)
				}
				revision = created.Revision
			}

			if _, err := store.PutConditional(ctx, key, []byte("replacement"), test.condition(revision)); !errors.Is(err, ports.ErrInvalidCondition) {
				t.Fatalf("PutConditional error = %v, want invalid condition", err)
			}
			if test.existing {
				if got := readObject(t, store, key); string(got) != "original" {
					t.Fatalf("invalid condition changed object to %q", got)
				}
			} else if _, err := store.Head(ctx, key); !errors.Is(err, ports.ErrNotFound) {
				t.Fatalf("invalid condition created object: %v", err)
			}
		})
	}
}

func TestStorePersistsAMutationUniqueRevisionWithEachObject(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	key := "capsule/latest.json"
	body := []byte("same pointer bytes")
	first, err := store.PutConditional(ctx, key, body, ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	readPersistedRevision := func() ports.Revision {
		t.Helper()
		value := make([]byte, 64)
		n, err := syscall.Getxattr(filepath.Join(root, filepath.FromSlash(key)), "user.camp.revision", value)
		if err != nil {
			t.Fatalf("read persisted revision: %v", err)
		}
		return ports.Revision(hex.EncodeToString(value[:n]))
	}
	if persisted := readPersistedRevision(); persisted != first.Revision {
		t.Fatalf("persisted revision = %q, want %q", persisted, first.Revision)
	}

	reopened, err := filebackend.New(root)
	if err != nil {
		t.Fatal(err)
	}
	reopenedMeta, err := reopened.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedMeta.Revision != first.Revision {
		t.Fatalf("revision changed after reopen: %q -> %q", first.Revision, reopenedMeta.Revision)
	}
	second, err := reopened.PutConditional(ctx, key, body, ports.WriteCondition{MatchRevision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision {
		t.Fatal("same-byte mutation reused its predecessor revision")
	}
	if persisted := readPersistedRevision(); persisted != second.Revision {
		t.Fatalf("replacement persisted revision = %q, want %q", persisted, second.Revision)
	}
}

func TestStoreSourceIdentityReturnsCanonicalObjectLocation(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	key := "capsule/generations/1.tar.zst"
	body := []byte("identity bytes")
	meta, err := store.PutImmutable(ctx, key, byteSource(body), digest(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	info, err := store.SourceIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "file" || info.Key != key || info.Revision != meta.Revision || info.Size != meta.Size || info.SHA256 != meta.SHA256 {
		t.Fatalf("identity mismatch\n got: %#v\nwant key=%q rev=%q size=%d sha=%q", info, meta.Key, meta.Revision, meta.Size, meta.SHA256)
	}
	expectedPath := filepath.Join(root, filepath.FromSlash(key))
	if info.CanonicalPath != expectedPath {
		t.Fatalf("canonical path = %q, want %q", info.CanonicalPath, expectedPath)
	}

	stat, err := os.Stat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type %T", stat.Sys())
	}
	if info.Device != uint64(raw.Dev) || info.Inode != raw.Ino {
		t.Fatalf("file identity mismatch: got dev=%d ino=%d want dev=%d ino=%d", info.Device, info.Inode, raw.Dev, raw.Ino)
	}
}

func TestStoreDoesNotDeleteAfterContextCancellation(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	created, err := store.PutConditional(ctx, "capsule/latest.json", []byte("pointer"), ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.DeleteConditional(canceled, "capsule/latest.json", created.Revision); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete after cancellation error = %v, want context canceled", err)
	}
	if got := readObject(t, store, "capsule/latest.json"); string(got) != "pointer" {
		t.Fatalf("canceled delete changed object to %q", got)
	}
}

func TestStoreChecksCancellationImmediatelyBeforeMutations(t *testing.T) {
	t.Run("immutable rename", func(t *testing.T) {
		store, _ := newStore(t)
		body := []byte("immutable generation")
		ctx, cancel := context.WithCancel(context.Background())
		_, err := store.PutImmutable(ctx, "capsule/generations/1.tar.zst", cancelOnCloseSource{body: body, cancel: cancel}, digest(body), int64(len(body)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PutImmutable error = %v, want context canceled", err)
		}
		if _, err := store.Head(context.Background(), "capsule/generations/1.tar.zst"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("canceled immutable write was published: %v", err)
		}
	})

	t.Run("conditional rename", func(t *testing.T) {
		store, _ := newStore(t)
		ctx := newCancelOnErrContext(4)
		_, err := store.PutConditional(ctx, "capsule/latest.json", []byte("pointer"), ports.WriteCondition{MustBeAbsent: true})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PutConditional error = %v after %d context checks, want context canceled", err, ctx.calls)
		}
		if _, err := store.Head(context.Background(), "capsule/latest.json"); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("canceled conditional write was published: %v", err)
		}
	})

	t.Run("conditional delete", func(t *testing.T) {
		store, _ := newStore(t)
		created, err := store.PutConditional(context.Background(), "capsule/latest.json", []byte("pointer"), ports.WriteCondition{MustBeAbsent: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx := newCancelOnErrContext(2)
		err = store.DeleteConditional(ctx, "capsule/latest.json", created.Revision)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DeleteConditional error = %v after %d context checks, want context canceled", err, ctx.calls)
		}
		if got := readObject(t, store, "capsule/latest.json"); string(got) != "pointer" {
			t.Fatalf("canceled conditional delete changed object to %q", got)
		}
	})
}

func TestStoreListPaginatesInDeterministicKeyOrder(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	want := make([]string, 105)
	for i := range want {
		want[i] = fmt.Sprintf("capsule/generations/%03d.json", len(want)-1-i)
		if _, err := store.PutConditional(ctx, want[i], []byte(want[i]), ports.WriteCondition{MustBeAbsent: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PutConditional(ctx, "other/generations/ignored.json", []byte("ignored"), ports.WriteCondition{MustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(want)

	var got []string
	var token string
	var firstToken string
	pages := 0
	for {
		items, next, err := store.List(ctx, "capsule/generations/", token)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, item := range items {
			got = append(got, item.Key)
		}
		if next == "" {
			break
		}
		if next == token {
			t.Fatalf("pagination token did not advance: %q", token)
		}
		if firstToken == "" {
			firstToken = next
		}
		token = next
	}
	if pages < 2 {
		t.Fatalf("List returned %d items in one page; pagination was not exercised", len(got))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keys differ\n got: %v\nwant: %v", got, want)
	}
	if _, _, err := store.List(ctx, "capsule/", "not-a-page-token"); !errors.Is(err, ports.ErrInvalidPageToken) {
		t.Fatalf("invalid token error = %v, want invalid page token", err)
	}
	if firstToken == "" {
		t.Fatal("paginated listing did not return a token")
	}
	for _, mismatchedPrefix := range []string{"capsule/", "capsule/generations/0"} {
		if _, _, err := store.List(ctx, mismatchedPrefix, firstToken); !errors.Is(err, ports.ErrInvalidPageToken) {
			t.Errorf("token accepted for mismatched prefix %q: %v", mismatchedPrefix, err)
		}
	}
	wrongVersion, err := json.Marshal(map[string]any{
		"version": 2,
		"prefix":  "capsule/generations/",
		"cursor":  "capsule/generations/099.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, invalidToken := range map[string]string{
		"legacy cursor":  base64.RawURLEncoding.EncodeToString([]byte("capsule/generations/099.json")),
		"malformed JSON": base64.RawURLEncoding.EncodeToString([]byte("not-json")),
		"wrong version":  base64.RawURLEncoding.EncodeToString(wrongVersion),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.List(ctx, "capsule/generations/", invalidToken); !errors.Is(err, ports.ErrInvalidPageToken) {
				t.Fatalf("invalid token error = %v, want invalid page token", err)
			}
		})
	}
}

func TestStoreRejectsTraversalAndSymlinkEscapes(t *testing.T) {
	store, root := newStore(t)
	ctx := context.Background()
	for _, key := range []string{"", "/absolute", "../outside", "capsule/../../outside", "capsule//latest.json", `capsule\latest.json`, "capsule/.camp-locks/object"} {
		if _, err := store.PutConditional(ctx, key, []byte("unsafe"), ports.WriteCondition{MustBeAbsent: true}); !errors.Is(err, ports.ErrInvalidKey) {
			t.Errorf("PutConditional(%q) error = %v, want invalid key", key, err)
		}
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "escape/secret"); !errors.Is(err, ports.ErrUnsafePath) {
		t.Fatalf("symlink Get error = %v, want unsafe path", err)
	}
	if _, err := store.PutConditional(ctx, "escape/new", []byte("outside"), ports.WriteCondition{MustBeAbsent: true}); !errors.Is(err, ports.ErrUnsafePath) {
		t.Fatalf("symlink PutConditional error = %v, want unsafe path", err)
	}
	if _, _, err := store.List(ctx, "", ""); !errors.Is(err, ports.ErrUnsafePath) {
		t.Fatalf("List with symlink error = %v, want unsafe path", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped root: %v", err)
	}
}

type helperProcess struct {
	cmd *exec.Cmd
	out bytes.Buffer
}

func startHelper(t *testing.T, env ...string) *helperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFileBackendHelperProcess$")
	cmd.Env = append(os.Environ(), append([]string{"CAMP_FILEBACKEND_HELPER=1"}, env...)...)
	child := &helperProcess{cmd: cmd}
	cmd.Stdout = &child.out
	cmd.Stderr = &child.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return child
}

func waitForFiles(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		all := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				all = false
				break
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper files: %v", paths)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStoreSerializesConditionalWritersAcrossProcesses(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "objects")
	start := filepath.Join(tmp, "start")
	const writers = 8
	children := make([]*helperProcess, writers)
	ready := make([]string, writers)
	for i := range children {
		ready[i] = filepath.Join(tmp, fmt.Sprintf("ready-%d", i))
		children[i] = startHelper(t,
			"CAMP_HELPER_OP=conditional-create",
			"CAMP_HELPER_ROOT="+root,
			"CAMP_HELPER_KEY=capsule/latest.json",
			fmt.Sprintf("CAMP_HELPER_BODY=writer-%d", i),
			"CAMP_HELPER_READY="+ready[i],
			"CAMP_HELPER_START="+start,
		)
	}
	waitForFiles(t, ready...)
	if err := os.WriteFile(start, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}

	created, conflicted := 0, 0
	for _, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%s", err, child.out.String())
		}
		switch {
		case strings.Contains(child.out.String(), "CAMP_RESULT=created"):
			created++
		case strings.Contains(child.out.String(), "CAMP_RESULT=conflict"):
			conflicted++
		default:
			t.Fatalf("helper produced no result: %s", child.out.String())
		}
	}
	if created != 1 || conflicted != writers-1 {
		t.Fatalf("created=%d conflicted=%d, want 1/%d", created, conflicted, writers-1)
	}
}

func countTemporaryFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".camp-tmp-") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStoreRecoversAnInterruptedTemporaryWrite(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "objects")
	ready := filepath.Join(tmp, "blocked")
	body := "partial generation bytes"
	child := startHelper(t,
		"CAMP_HELPER_OP=crash-immutable",
		"CAMP_HELPER_ROOT="+root,
		"CAMP_HELPER_KEY=capsule/generations/1.tar.zst",
		"CAMP_HELPER_BODY="+body,
		"CAMP_HELPER_READY="+ready,
	)
	waitForFiles(t, ready)
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.cmd.Wait(); err == nil {
		t.Fatal("crash helper exited successfully after SIGKILL")
	}
	if got := countTemporaryFiles(t, root); got == 0 {
		t.Fatal("helper did not leave a real interrupted temporary file")
	}

	store, err := filebackend.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(context.Background(), "capsule/generations/1.tar.zst"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("interrupted target became visible: %v", err)
	}
	payload := []byte(body)
	if _, err := store.PutImmutable(context.Background(), "capsule/generations/1.tar.zst", byteSource(payload), digest(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if got := countTemporaryFiles(t, root); got != 0 {
		t.Fatalf("successful retry left %d temporary files", got)
	}
}

type crashSource struct {
	body  []byte
	ready string
}

func (s crashSource) Open() (io.ReadCloser, error) {
	return &crashReader{body: s.body, ready: s.ready}, nil
}

type crashReader struct {
	body      []byte
	ready     string
	delivered bool
}

func (r *crashReader) Read(p []byte) (int, error) {
	if !r.delivered {
		r.delivered = true
		return copy(p, r.body), nil
	}
	if err := os.WriteFile(r.ready, []byte("blocked"), 0o600); err != nil {
		return 0, err
	}
	select {}
}

func (r *crashReader) Close() error { return nil }

func TestFileBackendHelperProcess(t *testing.T) {
	if os.Getenv("CAMP_FILEBACKEND_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := filebackend.New(os.Getenv("CAMP_HELPER_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := os.Getenv("CAMP_HELPER_KEY")
	body := []byte(os.Getenv("CAMP_HELPER_BODY"))

	switch os.Getenv("CAMP_HELPER_OP") {
	case "conditional-create":
		if err := os.WriteFile(os.Getenv("CAMP_HELPER_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFiles(t, os.Getenv("CAMP_HELPER_START"))
		_, err := store.PutConditional(ctx, key, body, ports.WriteCondition{MustBeAbsent: true})
		switch {
		case err == nil:
			fmt.Println("CAMP_RESULT=created")
		case errors.Is(err, ports.ErrConflict):
			fmt.Println("CAMP_RESULT=conflict")
		default:
			t.Fatal(err)
		}
	case "crash-immutable":
		_, err := store.PutImmutable(ctx, key, crashSource{body: body, ready: os.Getenv("CAMP_HELPER_READY")}, digest(body), int64(len(body)))
		t.Fatalf("crash write unexpectedly returned: %v", err)
	default:
		t.Fatalf("unknown helper operation %q", os.Getenv("CAMP_HELPER_OP"))
	}
}

var _ ports.ObjectStore = (*filebackend.Store)(nil)
