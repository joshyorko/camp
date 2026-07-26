package app

import (
	"context"
	"io"

	"github.com/joshyorko/camp/internal/campkit"
)

// CampKitExportResolver owns authoritative manifest and exact-generation
// source resolution. It must not move pointers or acquire leases.
type CampKitExportResolver interface {
	ResolveCampKitExport(context.Context, string) (campkit.Manifest, map[string]campkit.PayloadSource, CampKitSourceRevalidator, error)
}

type CampKitSourceRevalidator interface {
	RevalidateSources(context.Context) error
}

// ExportCampKit resolves one exact generation, validates its sources before
// transfer, streams to an atomic file, and validates again before publication.
func ExportCampKit(ctx context.Context, output, generation string, resolver CampKitExportResolver) error {
	if resolver == nil {
		return io.ErrClosedPipe
	}
	manifest, sources, revalidator, err := resolver.ResolveCampKitExport(ctx, generation)
	if err != nil {
		return err
	}
	if revalidator == nil {
		return io.ErrClosedPipe
	}
	if err := revalidator.RevalidateSources(ctx); err != nil {
		return err
	}
	return campkit.ExportFileWithBeforePublish(ctx, output, manifest, sources, func() error {
		return revalidator.RevalidateSources(ctx)
	})
}
