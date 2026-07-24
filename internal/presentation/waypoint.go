package presentation

const (
	colorReset  = "\x1b[0m"
	colorSky    = "\x1b[38;2;108;178;235m"
	colorPine   = "\x1b[38;2;58;110;70m"
	colorMeadow = "\x1b[38;2;140;188;92m"
	colorCanvas = "\x1b[38;2;238;215;169m"
	colorAmber  = "\x1b[38;2;255;171;45m"
	colorGreen  = "\x1b[38;2;102;214;86m"
	colorBlue   = "\x1b[38;2;56;155;255m"
	colorRed    = "\x1b[38;2;233;77;77m"
	colorDim    = "\x1b[38;2;110;118;129m"
)

type WaypointStatus int

const (
	WaypointPending WaypointStatus = iota
	WaypointActive
	WaypointCompleted
	WaypointFailed
)

func (status WaypointStatus) glyph() string {
	switch status {
	case WaypointCompleted:
		return "✓"
	case WaypointActive:
		return "◐"
	case WaypointFailed:
		return "✗"
	default:
		return "○"
	}
}

func (status WaypointStatus) color() string {
	switch status {
	case WaypointCompleted:
		return colorGreen
	case WaypointActive:
		return colorAmber
	case WaypointFailed:
		return colorRed
	default:
		return colorDim
	}
}

func (status WaypointStatus) paint(label string) string {
	return status.color() + status.glyph() + " " + label + colorReset
}

func waypointStatuses(completed, failedAt int) [4]WaypointStatus {
	var statuses [4]WaypointStatus
	for i := range statuses {
		switch {
		case failedAt >= 0 && i == failedAt:
			statuses[i] = WaypointFailed
		case failedAt >= 0 && i > failedAt:
			statuses[i] = WaypointPending
		case i < completed:
			statuses[i] = WaypointCompleted
		case i == completed:
			statuses[i] = WaypointActive
		default:
			statuses[i] = WaypointPending
		}
	}
	return statuses
}
