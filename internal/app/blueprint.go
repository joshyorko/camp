package app

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/domain"
)

type BlueprintSource interface {
	CurrentBlueprint(context.Context) (domain.CampBlueprint, error)
}

type BlueprintInspection struct {
	Blueprint domain.CampBlueprint `json:"blueprint"`
	Ref       domain.BlueprintRef  `json:"ref"`
}

type BlueprintInspector struct{ source BlueprintSource }

func NewBlueprintInspector(source BlueprintSource) *BlueprintInspector {
	return &BlueprintInspector{source: source}
}
func (i *BlueprintInspector) Inspect(ctx context.Context) (BlueprintInspection, error) {
	if i == nil || i.source == nil {
		return BlueprintInspection{}, errors.New("blueprint source is nil")
	}
	blueprint, err := i.source.CurrentBlueprint(ctx)
	if err != nil {
		return BlueprintInspection{}, err
	}
	if err := blueprint.Validate(); err != nil {
		return BlueprintInspection{}, err
	}
	ref, err := blueprint.Ref()
	if err != nil {
		return BlueprintInspection{}, err
	}
	return BlueprintInspection{Blueprint: blueprint, Ref: ref}, nil
}
