package setupui

// CampsiteFacts is the authoritative, already-sanitized data for a completed
// campsite. It mirrors the CLI's presentation.CampsiteModel fields so the rich
// renderer can draw the ready scene for an already-configured re-run of
// `camp setup` without depending on the CLI package.
type CampsiteFacts struct {
	DevPod      string
	Hauler      string
	Provider    string
	RuntimeKind string
	Context     string
	Capsule     string
	Source      string
	BackendKind string
	Storage     string
	NextCommand string
}

// RenderReadyCampsite composes the full "CAMP IS READY" scene for a completed
// campsite at the given size. It is a one-shot static frame (no event loop):
// callers print it directly, so the same terminal-native scene represents both
// a freshly finished setup and a re-run over existing configuration.
func RenderReadyCampsite(facts CampsiteFacts, w, h int, pal Palette, sprites map[string]Sprite) string {
	data := SceneData{
		Title:    "⌂ CAMP",
		Subtitle: "trailhead setup",
		Waypoints: [4]Waypoint{
			{Label: "TOOLCHAIN", Landmark: "crate", State: WaypointCompleted, Meta: []string{facts.DevPod, facts.Hauler}},
			{Label: "RUNTIME", Landmark: "helm", State: WaypointCompleted, Meta: []string{facts.Provider + " · " + facts.RuntimeKind, "context " + facts.Context}},
			{Label: "CAPSULE", Landmark: "tent", State: WaypointCompleted, Meta: []string{facts.Capsule, facts.Source}},
			{Label: "STORAGE", Landmark: "campfire", State: WaypointCompleted, Meta: []string{facts.BackendKind + " backend", facts.Storage}},
		},
		Ready:       true,
		ReadyLine:   facts.Capsule + " · " + facts.Provider + " · " + facts.BackendKind + " backend",
		NextCommand: facts.NextCommand,
	}
	return Compose(data, w, h, pal, sprites)
}
