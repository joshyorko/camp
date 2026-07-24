package setupui

// SampleFrame composes a Trailhead scene with deterministic sample data for a
// named state. It exists so scene review captures and golden tests exercise the
// exact production renderer (Compose) rather than a parallel mock. States:
// "configure", "progress", "ready", "failure".
func SampleFrame(state string, w, h int, pal Palette, sprites map[string]Sprite) string {
	base := [4]Waypoint{
		{Label: "TOOLCHAIN", Landmark: "crate", Meta: []string{"DevPod v0.26.1", "Hauler v2.0.2"}},
		{Label: "RUNTIME", Landmark: "helm", Meta: []string{"room-of-requirement", "context ror"}},
		{Label: "CAPSULE", Landmark: "tent", Meta: []string{"SecondBrain", "~/SecondBrain"}},
		{Label: "STORAGE", Landmark: "campfire", Meta: []string{"file backend", "generation ready"}},
	}
	data := SceneData{
		Title:    "⌂ CAMP",
		Subtitle: "trailhead setup",
	}
	switch state {
	case "configure":
		form := NewConfigForm(pal, map[string]string{
			"source": "~/SecondBrain", "capsule": "SecondBrain",
			"backend": "file://…", "provider": "docker", "context": "default",
		})
		form.SetWidth(min(w-6, 60))
		for i := range base {
			base[i].State = WaypointPending
		}
		data.Waypoints = base
		data.Foreground = form.View()
		data.HelpLine = "tab next · shift+tab prev · enter continue · esc cancel"
	case "progress":
		base[0].State = WaypointCompleted
		base[1].State = WaypointActive
		base[2].State = WaypointPending
		base[3].State = WaypointPending
		data.Waypoints = base
		data.HelpLine = "ctrl+c quit"
	case "failure":
		base[0].State = WaypointCompleted
		base[1].State = WaypointFailed
		base[2].State = WaypointPending
		base[3].State = WaypointPending
		data.Waypoints = base
		data.Failure = "devpod provider docker is unreachable"
		data.Recovery = "camp setup"
		data.HelpLine = "enter to exit · ctrl+c quit"
	default: // ready
		for i := range base {
			base[i].State = WaypointCompleted
		}
		data.Waypoints = base
		data.Ready = true
		data.ReadyLine = "SecondBrain · room-of-requirement · file backend"
		data.NextCommand = "camp open ~/SecondBrain"
		data.HelpLine = "enter to exit · ctrl+c quit"
	}
	return Compose(data, w, h, pal, sprites)
}
