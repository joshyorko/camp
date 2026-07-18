package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

const (
	sessionsDirectory = "sessions"
	creationDirectory = ".creating"
	journalFilename   = "journal.jsonl"
	snapshotFilename  = "snapshot.json"
)

type StoreOption func(*Store)

type CreateStep string

const (
	CreateAfterJournalSync  CreateStep = "after-journal-sync"
	CreateAfterSnapshotSync CreateStep = "after-snapshot-sync"
	CreateAfterCommitSync   CreateStep = "after-commit-sync"
)

func WithBeforeSnapshotRename(hook func() error) StoreOption {
	return func(store *Store) { store.beforeSnapshotRename = hook }
}

func WithCreateStepHook(hook func(CreateStep) error) StoreOption {
	return func(store *Store) { store.createStepHook = hook }
}

type Store struct {
	root                 string
	beforeSnapshotRename func() error
	createStepHook       func(CreateStep) error
}

type envelope struct {
	Kind     string                  `json:"kind"`
	Intent   *ports.IntentRecord     `json:"intent,omitempty"`
	Fact     *ports.FactRecord       `json:"fact,omitempty"`
	Snapshot *domain.JournalSnapshot `json:"snapshot,omitempty"`
}

func NewStore(root string, options ...StoreOption) (*Store, error) {
	if root == "" {
		return nil, errors.New("journal root is empty")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve journal root: %w", err)
	}
	store := &Store{root: canonical}
	for _, option := range options {
		option(store)
	}
	if err := os.MkdirAll(filepath.Join(canonical, sessionsDirectory, creationDirectory), 0o700); err != nil {
		return nil, fmt.Errorf("create journal root: %w", err)
	}
	return store, nil
}

func (s *Store) Create(ctx context.Context, snapshot domain.JournalSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionID(snapshot.SessionID); err != nil {
		return err
	}
	directory := s.sessionDirectory(snapshot.SessionID)
	if _, err := os.Lstat(directory); err == nil {
		return s.acceptExistingInitialSession(ctx, snapshot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect journal session %q: %w", snapshot.SessionID, err)
	}
	creatingRoot := filepath.Join(s.root, sessionsDirectory, creationDirectory)
	staging, err := os.MkdirTemp(creatingRoot, snapshot.SessionID+"-")
	if err != nil {
		return fmt.Errorf("stage journal session %q: %w", snapshot.SessionID, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o700); err != nil {
		return fmt.Errorf("secure staged journal session: %w", err)
	}
	if err := syncDirectory(creatingRoot); err != nil {
		return fmt.Errorf("sync journal creation root: %w", err)
	}
	log, err := os.OpenFile(filepath.Join(staging, journalFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create journal log: %w", err)
	}
	if err := log.Sync(); err != nil {
		_ = log.Close()
		return fmt.Errorf("sync journal log: %w", err)
	}
	if err := log.Close(); err != nil {
		return fmt.Errorf("close journal log: %w", err)
	}
	if err := syncDirectory(staging); err != nil {
		return fmt.Errorf("sync staged journal session: %w", err)
	}
	if err := s.runCreateStep(CreateAfterJournalSync); err != nil {
		cleanup = false
		return err
	}
	if err := s.writeSnapshot(staging, snapshot, false); err != nil {
		return err
	}
	if err := s.runCreateStep(CreateAfterSnapshotSync); err != nil {
		cleanup = false
		return err
	}
	if err := os.Rename(staging, directory); err != nil {
		if _, statErr := os.Lstat(directory); statErr == nil {
			return s.acceptExistingInitialSession(ctx, snapshot)
		}
		return fmt.Errorf("commit journal session %q: %w", snapshot.SessionID, err)
	}
	cleanup = false
	if err := syncDirectory(creatingRoot); err != nil {
		return fmt.Errorf("sync journal creation root after commit: %w", err)
	}
	if err := syncDirectory(filepath.Join(s.root, sessionsDirectory)); err != nil {
		return fmt.Errorf("sync committed journal session: %w", err)
	}
	return s.runCreateStep(CreateAfterCommitSync)
}

func (s *Store) RecordIntent(ctx context.Context, intent ports.IntentRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateIntent(intent); err != nil {
		return err
	}
	return s.append(intent.SessionID, envelope{Kind: "intent", Intent: &intent})
}

func (s *Store) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFact(fact, snapshot.SessionID); err != nil {
		return err
	}
	guard, err := os.OpenFile(filepath.Join(s.sessionDirectory(fact.SessionID), journalFilename), os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open journal fact lock: %w", err)
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock journal fact: %w", err)
	}
	defer syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
	current, _, err := s.Load(ctx, fact.SessionID)
	if err != nil {
		return err
	}
	composed := composeFactSnapshot(current, snapshot, fact.Transition)
	reduced, err := reduceFact(composed, fact)
	if err != nil {
		return err
	}
	if err := s.append(fact.SessionID, envelope{Kind: "fact", Fact: &fact, Snapshot: &reduced}); err != nil {
		return err
	}
	return s.writeSnapshot(s.sessionDirectory(fact.SessionID), reduced, true)
}

func composeFactSnapshot(current, candidate domain.JournalSnapshot, transition string) domain.JournalSnapshot {
	switch transition {
	case "LeaseRenewed":
		current.Lease = candidate.Lease
		return current
	case "ServingContentRefreshed":
		current.Services = candidate.Services
		return current
	case "ServiceStart", "ServiceRestart":
		if !reflect.DeepEqual(current.Services, candidate.Services) {
			current.Services = candidate.Services
			return current
		}
	case "WorkspaceMirrored", "WorkspaceImagesInventoried", "RegistrySnapshotSealed", "RootSnapshotStable", "GenerationUploaded", "PointerCommitted":
		candidate.Lease = current.Lease
	}
	return candidate
}

func (s *Store) Load(ctx context.Context, sessionID string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	if err := ctx.Err(); err != nil {
		return domain.JournalSnapshot{}, nil, err
	}
	if err := validateSessionID(sessionID); err != nil {
		return domain.JournalSnapshot{}, nil, err
	}
	directory := s.sessionDirectory(sessionID)
	snapshot, err := readSnapshot(filepath.Join(directory, snapshotFilename))
	if err != nil {
		return domain.JournalSnapshot{}, nil, err
	}
	file, err := os.Open(filepath.Join(directory, journalFilename))
	if err != nil {
		return domain.JournalSnapshot{}, nil, fmt.Errorf("open journal log: %w", err)
	}
	defer file.Close()

	pending := make(map[string]ports.IntentRecord)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var entry envelope
			if err := json.Unmarshal(line, &entry); err != nil {
				return domain.JournalSnapshot{}, nil, fmt.Errorf("decode journal entry: %w", err)
			}
			switch entry.Kind {
			case "intent":
				if entry.Intent == nil || entry.Intent.SessionID != sessionID {
					return domain.JournalSnapshot{}, nil, errors.New("invalid intent journal entry")
				}
				pending[entry.Intent.ID] = *entry.Intent
			case "fact":
				if entry.Fact == nil || entry.Snapshot == nil || entry.Fact.SessionID != sessionID {
					return domain.JournalSnapshot{}, nil, errors.New("invalid fact journal entry")
				}
				if _, ok := pending[entry.Fact.IntentID]; !ok {
					return domain.JournalSnapshot{}, nil, fmt.Errorf("fact references unknown intent %q", entry.Fact.IntentID)
				}
				delete(pending, entry.Fact.IntentID)
				snapshot = *entry.Snapshot
			default:
				return domain.JournalSnapshot{}, nil, fmt.Errorf("unknown journal entry kind %q", entry.Kind)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return domain.JournalSnapshot{}, nil, fmt.Errorf("read journal log: %w", readErr)
		}
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ports.PendingIntent, 0, len(ids))
	for _, id := range ids {
		result = append(result, ports.PendingIntent{Intent: pending[id]})
	}
	return snapshot, result, nil
}

func (s *Store) List(ctx context.Context) ([]domain.JournalSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, sessionsDirectory))
	if err != nil {
		return nil, fmt.Errorf("list journal sessions: %w", err)
	}
	result := make([]domain.JournalSnapshot, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == creationDirectory {
			continue
		}
		snapshot, _, err := s.Load(ctx, entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SessionID < result[j].SessionID })
	return result, nil
}

func (s *Store) acceptExistingInitialSession(ctx context.Context, expected domain.JournalSnapshot) error {
	journalPath := filepath.Join(s.sessionDirectory(expected.SessionID), journalFilename)
	info, err := os.Stat(journalPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return fmt.Errorf("journal session %q already exists", expected.SessionID)
	}
	actual, pending, err := s.Load(ctx, expected.SessionID)
	if err != nil || len(pending) != 0 {
		return fmt.Errorf("journal session %q already exists", expected.SessionID)
	}
	wantBody, wantErr := json.Marshal(expected)
	actualBody, actualErr := json.Marshal(actual)
	if wantErr != nil || actualErr != nil || string(wantBody) != string(actualBody) {
		return fmt.Errorf("journal session %q already exists with different initial state", expected.SessionID)
	}
	return nil
}

func (s *Store) runCreateStep(step CreateStep) error {
	if s.createStepHook == nil {
		return nil
	}
	return s.createStepHook(step)
}

func (s *Store) append(sessionID string, entry envelope) error {
	path := filepath.Join(s.sessionDirectory(sessionID), journalFilename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open journal append: %w", err)
	}
	body, err := json.Marshal(entry)
	if err == nil {
		_, err = file.Write(append(body, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("append and sync journal: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close journal: %w", closeErr)
	}
	return nil
}

func (s *Store) writeSnapshot(directory string, snapshot domain.JournalSnapshot, replace bool) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal journal snapshot: %w", err)
	}
	temporary, err := os.OpenFile(filepath.Join(directory, ".snapshot.partial"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot partial: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		cleanup()
		return fmt.Errorf("write snapshot partial: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return fmt.Errorf("sync snapshot partial: %w", err)
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close snapshot partial: %w", err)
	}
	if s.beforeSnapshotRename != nil {
		if err := s.beforeSnapshotRename(); err != nil {
			cleanup()
			return err
		}
	}
	final := filepath.Join(directory, snapshotFilename)
	if !replace {
		if _, err := os.Stat(final); err == nil {
			cleanup()
			return os.ErrExist
		}
	}
	if err := os.Rename(temporaryPath, final); err != nil {
		cleanup()
		return fmt.Errorf("commit journal snapshot: %w", err)
	}
	if err := os.Chmod(final, 0o600); err != nil {
		return fmt.Errorf("secure journal snapshot: %w", err)
	}
	return syncDirectory(directory)
}

func readSnapshot(path string) (domain.JournalSnapshot, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return domain.JournalSnapshot{}, fmt.Errorf("read journal snapshot: %w", err)
	}
	var snapshot domain.JournalSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return domain.JournalSnapshot{}, fmt.Errorf("decode journal snapshot: %w", err)
	}
	return snapshot, nil
}

func reduceFact(snapshot domain.JournalSnapshot, fact ports.FactRecord) (domain.JournalSnapshot, error) {
	if fact.Transition == "PointerCommitted" {
		if !completePointerCommit(snapshot, fact.Pointer) {
			return domain.JournalSnapshot{}, errors.New("PointerCommitted fact lacks complete pointer")
		}
		generation := fact.Pointer.Pointer.Generation
		pointer := fact.Pointer.Pointer
		snapshot.CurrentBase = &generation
		snapshot.CurrentPointer = &pointer
		snapshot.ExpectedPointerRevision = fact.Pointer.Revision
	}
	snapshot.UpdatedAt = fact.Timestamp
	return snapshot, nil
}

func completePointerCommit(snapshot domain.JournalSnapshot, commit *ports.PointerCommit) bool {
	if commit == nil || commit.Revision == "" {
		return false
	}
	pointer := commit.Pointer
	return pointer.SchemaVersion == domain.SchemaVersion &&
		pointer.Capsule != "" && pointer.Capsule == snapshot.Capsule &&
		pointer.Lineage == snapshot.Lineage && pointer.Lineage.Branch != "" &&
		pointer.Generation.Generation != 0 && len(pointer.Generation.ArchiveSHA256) == 64 &&
		pointer.ObjectKey != "" && pointer.Size >= 0 && !pointer.CreatedAt.IsZero() && pointer.SessionID != ""
}

func validateIntent(intent ports.IntentRecord) error {
	if err := validateSessionID(intent.SessionID); err != nil {
		return err
	}
	if intent.ID == "" || strings.ContainsAny(intent.ID, "/\\\x00") || intent.Transition == "" || intent.Attempt < 1 || intent.Timestamp.IsZero() {
		return errors.New("invalid journal intent")
	}
	if len(intent.Input) > 0 && !json.Valid(intent.Input) {
		return errors.New("invalid journal intent input")
	}
	return nil
}

func validateFact(fact ports.FactRecord, snapshotID string) error {
	if err := validateSessionID(fact.SessionID); err != nil {
		return err
	}
	if fact.SessionID != snapshotID || fact.IntentID == "" || fact.Transition == "" || fact.Timestamp.IsZero() {
		return errors.New("invalid journal fact")
	}
	if len(fact.Output) > 0 && !json.Valid(fact.Output) {
		return errors.New("invalid journal fact output")
	}
	return nil
}

func validateSessionID(sessionID string) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00") {
		return fmt.Errorf("invalid journal session id %q", sessionID)
	}
	return nil
}

func (s *Store) sessionDirectory(sessionID string) string {
	return filepath.Join(s.root, sessionsDirectory, sessionID)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
