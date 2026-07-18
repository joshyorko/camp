package presentation

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/joshyorko/camp/internal/app"
)

const SchemaVersion = 1

type Failure struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	NextCommands []string `json:"nextCommands,omitempty"`
}

type sessionsEnvelope struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	Sessions      []app.SessionReadModel `json:"sessions"`
}

type failureEnvelope struct {
	SchemaVersion int     `json:"schemaVersion"`
	Kind          string  `json:"kind"`
	Error         Failure `json:"error"`
}

func MarshalSessionsJSON(models []app.SessionReadModel) ([]byte, error) {
	sessions := append([]app.SessionReadModel(nil), models...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	for index := range sessions {
		sessions[index] = redactSession(sessions[index])
		sessions[index].Services = append([]app.ServiceReadModel(nil), sessions[index].Services...)
		sort.Slice(sessions[index].Services, func(i, j int) bool { return sessions[index].Services[i].Name < sessions[index].Services[j].Name })
	}
	return marshalEnvelope(sessionsEnvelope{SchemaVersion: SchemaVersion, Kind: "sessions", Sessions: sessions})
}

func MarshalFailureJSON(failure Failure) ([]byte, error) {
	if failure.Code == "" {
		return nil, errors.New("failure code is empty")
	}
	failure.Message = redact(failure.Message)
	failure.NextCommands = append([]string(nil), failure.NextCommands...)
	sort.Strings(failure.NextCommands)
	for index := range failure.NextCommands {
		failure.NextCommands[index] = redact(failure.NextCommands[index])
	}
	return marshalEnvelope(failureEnvelope{SchemaVersion: SchemaVersion, Kind: "error", Error: failure})
}

func marshalEnvelope(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func redactSession(model app.SessionReadModel) app.SessionReadModel {
	model.ID = redact(model.ID)
	model.Capsule = redact(model.Capsule)
	model.Branch = redact(model.Branch)
	model.Root = redact(model.Root)
	model.WorkspaceID = redact(model.WorkspaceID)
	model.Provider = redact(model.Provider)
	model.Cleanup.Message = redact(model.Cleanup.Message)
	model.Recovery.Command = redact(model.Recovery.Command)
	for index := range model.Services {
		model.Services[index].Name = redact(model.Services[index].Name)
	}
	return model
}
