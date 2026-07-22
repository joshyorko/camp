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

type CampsiteOptions struct {
	Color bool
	Width int
}

func RenderCampsite(writer io.Writer, model CampsiteModel, options CampsiteOptions) error {
	if err := validateCampsiteModel(model); err != nil {
		return err
	}
	if options.Color && options.Width >= 80 {
		_, err := io.WriteString(writer, renderColorCampsite(model))
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

func renderColorCampsite(model CampsiteModel) string {
	const (
		reset  = "\x1b[0m"
		sky    = "\x1b[38;2;78;163;235m"
		pine   = "\x1b[38;2;69;119;74m"
		canvas = "\x1b[38;2;238;215;169m"
		amber  = "\x1b[38;2;255;171;45m"
		green  = "\x1b[38;2;102;214;86m"
		blue   = "\x1b[38;2;56;155;255m"
	)
	return fmt.Sprintf(
		"%s        ·          ✦                ·            ✦          ·%s\n"+
			"%s      /\\       /\\          /\\        /\\       /\\%s\n"+
			"%s  ✓ TOOLCHAIN        ✓ RUNTIME        ✓ CAPSULE        ✓ STORAGE%s\n"+
			"%s  %s %s · %s %s%s\n"+
			"%s  %s · %s · context %s%s\n"+
			"%s  %s · %s%s\n"+
			"%s  %s backend · %s%s\n"+
			"%s  ━━━━━━━━◆━━━━━━━━━━◆━━━━━━━━━━◆━━━━━━━━━━🔥%s\n\n"+
			"%s                 CAMP IS READY%s\n"+
			"%s                 > %s%s\n",
		sky, reset,
		pine, reset,
		green, reset,
		canvas, model.DevPod.Name, model.DevPod.Version, model.Hauler.Name, model.Hauler.Version, reset,
		canvas, model.Provider, model.RuntimeKind, model.Context, reset,
		canvas, model.Capsule, model.Source, reset,
		canvas, model.BackendKind, model.Storage, reset,
		amber, reset,
		amber, reset,
		blue, model.NextCommand, reset,
	)
}
