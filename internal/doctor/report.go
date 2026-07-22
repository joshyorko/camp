package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const SchemaVersion = 1

type Status string

const (
	StatusHealthy              Status = "healthy"
	StatusDegraded             Status = "degraded"
	StatusBlocked              Status = "blocked"
	StatusSkippedNotConfigured Status = "skipped-not-configured"
)

type Result struct {
	Capability  string            `json:"capability"`
	Status      Status            `json:"status"`
	Code        string            `json:"code"`
	Summary     string            `json:"summary"`
	Evidence    map[string]string `json:"evidence,omitempty"`
	Remediation string            `json:"remediation,omitempty"`
}

type Report struct {
	SchemaVersion int      `json:"schemaVersion"`
	Kind          string   `json:"kind"`
	Status        Status   `json:"status"`
	Results       []Result `json:"results"`
}

func NewReport(results []Result) Report {
	ordered := append([]Result(nil), results...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Capability < ordered[j].Capability })
	status := StatusHealthy
	for _, result := range ordered {
		if result.Status == StatusBlocked {
			status = StatusBlocked
			break
		}
		if result.Status == StatusDegraded {
			status = StatusDegraded
		}
	}
	return Report{SchemaVersion: SchemaVersion, Kind: "doctor", Status: status, Results: ordered}
}

func (r Report) Blocked() bool { return r.Status == StatusBlocked }

func RenderJSON(output io.Writer, report Report) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func RenderHuman(output io.Writer, report Report) error {
	if _, err := fmt.Fprintf(output, "Camp doctor: %s\n", report.Status); err != nil {
		return err
	}
	for _, result := range report.Results {
		if _, err := fmt.Fprintf(output, "%s  %s  %s  %s\n", strings.ToUpper(string(result.Status)), result.Capability, result.Code, result.Summary); err != nil {
			return err
		}
		if result.Remediation != "" {
			if _, err := fmt.Fprintf(output, "  remediation: %s\n", result.Remediation); err != nil {
				return err
			}
		}
	}
	return nil
}
