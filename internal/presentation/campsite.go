package presentation

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
)

type ToolIdentity struct {
	Name    string
	Version string
}

type CampsiteModel struct {
	DevPod      ToolIdentity
	Hauler      ToolIdentity
	Provider    string
	RuntimeKind string
	Context     string
	Capsule     string
	Source      string
	BackendKind string
	Storage     string
	NextCommand string
}

type ScreenSize struct {
	Width  int
	Height int
}

type CampsiteOptions struct {
	Color  bool
	Width  int
	Height int
}

func RenderCampsite(writer io.Writer, model CampsiteModel, options CampsiteOptions) error {
	if err := validateCampsiteModel(model); err != nil {
		return err
	}
	if canRenderColorScene(options.Color, ScreenSize{Width: options.Width, Height: options.Height}) {
		_, err := io.WriteString(writer, renderColorCampsite(model, ScreenSize{Width: options.Width, Height: options.Height}))
		return err
	}
	_, err := io.WriteString(writer, renderPlainCampsite(model))
	return err
}

func validateCampsiteModel(model CampsiteModel) error {
	values := []struct {
		name  string
		value string
	}{
		{"devpod version", model.DevPod.Version},
		{"devpod name", model.DevPod.Name},
		{"hauler version", model.Hauler.Version},
		{"hauler name", model.Hauler.Name},
		{"provider", model.Provider},
		{"runtime kind", model.RuntimeKind},
		{"context", model.Context},
		{"capsule", model.Capsule},
		{"source", model.Source},
		{"backend kind", model.BackendKind},
		{"storage state", model.Storage},
		{"next command", model.NextCommand},
	}
	for _, field := range values {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("campsite metadata requires %s", field.name)
		}
		if unsafeCampsiteValue(field.value) {
			return fmt.Errorf("campsite metadata contains unsafe %s", field.name)
		}
	}
	return nil
}

func unsafeCampsiteValue(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(key)
		for _, marker := range []string{"token", "secret", "password", "credential", "access_key"} {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
	}
	return false
}

func canRenderColorScene(color bool, size ScreenSize) bool {
	return color && size.Width >= 80 && (size.Height == 0 || size.Height >= minCompactSceneHeight)
}

func renderPlainCampsite(model CampsiteModel) string {
	return fmt.Sprintf(
		"TOOLCHAIN  %s %s · %s %s\n"+
			"RUNTIME    %s · %s · context %s\n"+
			"CAPSULE    %s · %s\n"+
			"STORAGE    %s backend · %s\n"+
			"CAMP IS READY\n"+
			"> %s\n",
		model.DevPod.Name, model.DevPod.Version, model.Hauler.Name, model.Hauler.Version,
		model.Provider, model.RuntimeKind, model.Context,
		model.Capsule, model.Source,
		model.BackendKind, model.Storage,
		model.NextCommand,
	)
}

func renderColorCampsite(model CampsiteModel, size ScreenSize) string {
	return composeScene(model, waypointStatuses(len(setupWaypoints), -1), size, true, nil)
}
