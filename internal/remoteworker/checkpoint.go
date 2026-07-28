package remoteworker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/registry"
)

type ServiceCheckpointEvidence struct {
	Token    string                     `json:"token"`
	Services []domain.ServiceUnitRecord `json:"services"`
}

type CheckpointReceipt struct {
	SchemaVersion uint32                     `json:"schemaVersion"`
	Status        string                     `json:"status"`
	SessionID     string                     `json:"sessionId"`
	AttemptID     string                     `json:"attemptId"`
	Services      ServiceCheckpointEvidence  `json:"services"`
	Registry      registry.Snapshot          `json:"registry"`
	Images        domain.ImageInventory      `json:"images"`
	Root          archiveadapter.ArchiveInfo `json:"root"`
	Store         haulkit.StoreIdentity      `json:"store"`
	Kit           haulkit.Artifact           `json:"kit"`
	AllowList     []string                   `json:"allowList"`
}

type checkpointRuntime interface {
	Verify(context.Context, Request) error
	Observe(context.Context, Request) (CheckpointReceipt, bool, error)
	Quiesce(context.Context, Request) (ServiceCheckpointEvidence, error)
	ReleaseBarrier(context.Context, Request, ServiceCheckpointEvidence) error
	CutRegistry(context.Context, Request, ServiceCheckpointEvidence) (registry.Snapshot, error)
	Inventory(context.Context, Request, registry.Snapshot) (domain.ImageInventory, error)
	ArchiveRoot(context.Context, Request, registry.Snapshot) (archiveadapter.ArchiveInfo, error)
	BuildStore(context.Context, Request, archiveadapter.ArchiveInfo, domain.ImageInventory) (haulkit.StoreIdentity, error)
	BuildKit(context.Context, Request, archiveadapter.ArchiveInfo, haulkit.StoreIdentity) (haulkit.Artifact, error)
	Publish(context.Context, Request, CheckpointReceipt) (CheckpointReceipt, error)
	Resume(context.Context, Request, ServiceCheckpointEvidence) error
}

func checkpoint(ctx context.Context, request Request, runtime checkpointRuntime) (result CheckpointReceipt, resultErr error) {
	if runtime == nil || request.Checkpoint == nil {
		return CheckpointReceipt{}, errors.New("remote checkpoint dependencies or request are incomplete")
	}
	if err := runtime.Verify(ctx, request); err != nil {
		return CheckpointReceipt{}, fmt.Errorf("verify remote checkpoint authority: %w", err)
	}
	if observed, ok, err := runtime.Observe(ctx, request); err != nil {
		return CheckpointReceipt{}, fmt.Errorf("observe remote checkpoint attempt: %w", err)
	} else if ok {
		if err := validateCheckpointReceipt(request, observed); err != nil {
			return CheckpointReceipt{}, err
		}
		if !request.Checkpoint.Close {
			if err := runtime.Resume(ctx, request, observed.Services); err != nil {
				return CheckpointReceipt{}, fmt.Errorf("resume exact remote services: %w", err)
			}
		}
		return observed, nil
	}
	services, err := runtime.Quiesce(ctx, request)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("establish remote registry write barrier: %w", err)
	}
	if services.Token == "" || len(services.Services) != 2 {
		return CheckpointReceipt{}, errors.New("remote checkpoint service evidence is incomplete")
	}
	barrierHeld := true
	defer func() {
		if barrierHeld {
			resultErr = errors.Join(resultErr, runtime.ReleaseBarrier(context.WithoutCancel(ctx), request, services))
		}
	}()
	cut, err := runtime.CutRegistry(ctx, request, services)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("create immutable remote registry cut: %w", err)
	}
	inventory, err := runtime.Inventory(ctx, request, cut)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("inventory remote tagged images: %w", err)
	}
	if err := runtime.ReleaseBarrier(context.WithoutCancel(ctx), request, services); err != nil {
		return CheckpointReceipt{}, fmt.Errorf("release remote registry write barrier: %w", err)
	}
	barrierHeld = false
	root, err := runtime.ArchiveRoot(ctx, request, cut)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("archive remote workspace root: %w", err)
	}
	store, err := runtime.BuildStore(ctx, request, root, inventory)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("build and validate remote Hauler store: %w", err)
	}
	kit, err := runtime.BuildKit(ctx, request, root, store)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("build and verify return Camp Hauler Kit v1: %w", err)
	}
	allowList := checkpointAllowList(request.Checkpoint.AttemptID, kit.Chunks)
	receipt := CheckpointReceipt{
		SchemaVersion: ProtocolSchemaVersion, Status: "prepared", SessionID: request.SessionID,
		AttemptID: request.Checkpoint.AttemptID, Services: services, Registry: cut,
		Images: inventory, Root: root, Store: store, Kit: kit, AllowList: allowList,
	}
	if err := validateCheckpointReceipt(request, receipt); err != nil {
		return CheckpointReceipt{}, err
	}
	published, err := runtime.Publish(context.WithoutCancel(ctx), request, receipt)
	if err != nil {
		return CheckpointReceipt{}, fmt.Errorf("durably publish remote checkpoint receipt: %w", err)
	}
	if !reflect.DeepEqual(published, CheckpointReceipt{}) {
		if err := validateCheckpointReceipt(request, published); err != nil {
			return CheckpointReceipt{}, err
		}
		if !reflect.DeepEqual(published, receipt) {
			return CheckpointReceipt{}, errors.New("published remote checkpoint receipt differs from prepared attempt")
		}
		receipt = published
	}
	if !request.Checkpoint.Close {
		if err := runtime.Resume(ctx, request, services); err != nil {
			return CheckpointReceipt{}, fmt.Errorf("resume exact remote services: %w", err)
		}
	}
	return receipt, nil
}

func checkpointAllowList(attemptID string, chunks []haulkit.ChunkIdentity) []string {
	result := []string{attemptID + "/camp-hauler-kit.json"}
	for _, chunk := range chunks {
		result = append(result, attemptID+"/"+chunk.Name)
	}
	sort.Strings(result)
	return result
}

func validateCheckpointReceipt(request Request, receipt CheckpointReceipt) error {
	if receipt.SchemaVersion != ProtocolSchemaVersion || receipt.Status != "prepared" ||
		receipt.SessionID != request.SessionID || request.Checkpoint == nil ||
		receipt.AttemptID != request.Checkpoint.AttemptID || receipt.Services.Token == "" ||
		receipt.Registry.Root == "" || receipt.Root.Path == "" || receipt.Root.Size <= 0 ||
		!validDigest(receipt.Root.SHA256) || !validDigest(receipt.Store.IndexSHA256) ||
		receipt.Store.HaulerVersion == "" || receipt.Kit.ManifestPath == "" ||
		!validDigest(receipt.Kit.ManifestSHA256) || !validDigest(receipt.Kit.SHA256) ||
		receipt.Kit.Size <= 0 || len(receipt.Kit.Chunks) == 0 {
		return errors.New("remote checkpoint receipt identity is incomplete")
	}
	return validateCheckpointAllowList(receipt.AttemptID, receipt.Kit.Chunks, receipt.AllowList)
}

func validateCheckpointAllowList(attemptID string, chunks []haulkit.ChunkIdentity, allow []string) error {
	if !safeSegment(attemptID) {
		return errors.New("remote checkpoint allow-list attempt is unsafe")
	}
	expected := checkpointAllowList(attemptID, chunks)
	if !reflect.DeepEqual(allow, expected) {
		return errors.New("remote checkpoint fileserver allow-list differs from immutable manifest and chunks")
	}
	for _, item := range allow {
		if filepath.IsAbs(item) || strings.Contains(item, "\\") || strings.Contains(item, "..") ||
			strings.HasSuffix(item, "/") || filepath.Clean(item) != item ||
			!strings.HasPrefix(item, attemptID+"/") ||
			strings.HasSuffix(item, ".tar.zst") {
			return errors.New("remote checkpoint fileserver allow-list contains an unsafe exposure")
		}
	}
	return nil
}
