package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/campkit"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

var ErrCampKitIncompleteClosure = errors.New("incomplete CampKit closure")

type CampKitIncompleteClosureError struct {
	Capsule    string
	Branch     string
	Generation domain.GenerationRef
	Missing    []string
}

func (e *CampKitIncompleteClosureError) Error() string {
	return fmt.Sprintf("%v for %s/%s generation %d-%s: missing authoritative %s", ErrCampKitIncompleteClosure, e.Capsule, e.Branch, e.Generation.Generation, e.Generation.ArchiveSHA256, strings.Join(e.Missing, ", "))
}

func (e *CampKitIncompleteClosureError) Unwrap() error { return ErrCampKitIncompleteClosure }

type productionCampKitResolver struct {
	generations *coordination.GenerationRepository
	capsule     string
	lineage     domain.Lineage
}

func (r productionCampKitResolver) ResolveCampKitExport(ctx context.Context, value string) (campkit.Manifest, map[string]campkit.PayloadSource, app.CampKitSourceRevalidator, error) {
	ref, err := parseKitGenerationRef(value)
	if err != nil {
		return campkit.Manifest{}, nil, nil, err
	}
	exact, err := r.generations.ResolveExactGeneration(ctx, r.capsule, r.lineage, ref)
	if err != nil {
		return campkit.Manifest{}, nil, nil, err
	}
	return campkit.Manifest{}, nil, exact, &CampKitIncompleteClosureError{
		Capsule: r.capsule, Branch: r.lineage.Branch, Generation: ref,
		Missing: []string{"camp executable", "runtime", "devpod provider", "devpod tool", "hauler tool", "Room image"},
	}
}

func parseKitGenerationRef(value string) (domain.GenerationRef, error) {
	number, digest, ok := strings.Cut(value, "-")
	if !ok || number == "" || len(digest) != 64 || digest != strings.ToLower(digest) {
		return domain.GenerationRef{}, fmt.Errorf("invalid exact generation reference %q; want NUMBER-SHA256", value)
	}
	generation, err := strconv.ParseUint(number, 10, 64)
	if err != nil || generation == 0 {
		return domain.GenerationRef{}, fmt.Errorf("invalid exact generation reference %q; want NUMBER-SHA256", value)
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 {
		return domain.GenerationRef{}, fmt.Errorf("invalid exact generation reference %q; want NUMBER-SHA256", value)
	}
	return domain.GenerationRef{Generation: generation, ArchiveSHA256: digest}, nil
}

type KitExportReceipt struct {
	Generation string `json:"generation"`
	Output     string `json:"output"`
}

func (p *ProductionLifecycle) KitExport(ctx context.Context, request KitExportRequest, mode OutputMode, out io.Writer) error {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return err
	}
	store, err := objectstore.New(ctx, base.backend, objectstore.Options{})
	if err != nil {
		return err
	}
	resolver := productionCampKitResolver{
		generations: coordination.NewGenerationRepository(store),
		capsule:     base.runtime.Capsule,
		lineage:     domain.Lineage{Branch: "main"},
	}
	if err := app.ExportCampKit(ctx, request.Output, request.Generation, resolver); err != nil {
		return err
	}
	return writeKitExportReceipt(out, mode, KitExportReceipt{Generation: request.Generation, Output: request.Output})
}

func writeKitExportReceipt(out io.Writer, mode OutputMode, result KitExportReceipt) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "kit-export", result, "")
	}
	_, err := fmt.Fprintf(out, "exported generation %s to %s\n", result.Generation, result.Output)
	return err
}
