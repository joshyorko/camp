package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/campkit"
)

type recordingCampKitExportResolver struct {
	calls int
}

func (r *recordingCampKitExportResolver) ResolveCampKitExport(context.Context, string) (campkit.Manifest, map[string]campkit.PayloadSource, CampKitSourceRevalidator, error) {
	r.calls++
	return campkit.Manifest{}, nil, nil, errors.New("resolver must not be called")
}

func TestExportCampKitRejectsMissingRequestFieldsBeforeResolvingSources(t *testing.T) {
	t.Parallel()
	for _, request := range []struct {
		name       string
		output     string
		generation string
	}{
		{name: "output", generation: "42:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "generation", output: "kit.campkit"},
	} {
		request := request
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()
			resolver := &recordingCampKitExportResolver{}
			err := ExportCampKit(context.Background(), request.output, request.generation, resolver)
			if !errors.Is(err, ErrCampKitExportRequest) {
				t.Fatalf("ExportCampKit() error = %v, want ErrCampKitExportRequest", err)
			}
			if resolver.calls != 0 {
				t.Fatalf("resolver calls = %d, want 0", resolver.calls)
			}
		})
	}
}
