package presentation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/app"
)

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

var (
	assignmentSecret = regexp.MustCompile(`(?i)\b(token|password|secret|credential)=([^\s"']+)`)
	urlCredentials   = regexp.MustCompile(`://[^/@\s"']+@`)
)

func HumanStream(failed bool) Stream {
	if failed {
		return StreamStderr
	}
	return StreamStdout
}

func JSONStream() Stream { return StreamStdout }

func RenderSessionsHuman(models []app.SessionReadModel) string {
	sessions := append([]app.SessionReadModel(nil), models...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	var output strings.Builder
	fmt.Fprintf(&output, "%-13s %-8s %-8s %-11s %-20s %-12s %-10s %s\n", "SESSION", "CAPSULE", "BRANCH", "STATE", "SERVICES", "PUBLICATION", "CLEANUP", "RECOVERY")
	for _, session := range sessions {
		session = redactSession(session)
		services := append([]app.ServiceReadModel(nil), session.Services...)
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		serviceValues := make([]string, 0, len(services))
		for _, service := range services {
			serviceValues = append(serviceValues, service.Name+"="+string(service.Liveness))
		}
		serviceText := "-"
		if len(serviceValues) > 0 {
			serviceText = strings.Join(serviceValues, ",")
		}
		publication := string(session.Publication.Condition)
		if session.Publication.Generation != 0 {
			publication += fmt.Sprintf(":%d", session.Publication.Generation)
		}
		fmt.Fprintf(&output, "%-13s %-8s %-8s %-11s %-20s %-12s %-10s %s\n", session.ID, session.Capsule, session.Branch, session.State, serviceText, publication, session.Cleanup.Condition, session.Recovery.Condition)
	}
	return output.String()
}

func RenderFailureHuman(failure Failure) string {
	commands := append([]string(nil), failure.NextCommands...)
	sort.Strings(commands)
	var output strings.Builder
	fmt.Fprintf(&output, "error [%s]: %s\n", redact(failure.Code), redact(failure.Message))
	for _, command := range commands {
		fmt.Fprintf(&output, "next: %s\n", redact(command))
	}
	return output.String()
}

func redact(value string) string {
	value = assignmentSecret.ReplaceAllString(value, "$1=[REDACTED]")
	return urlCredentials.ReplaceAllString(value, "://[REDACTED]@")
}
