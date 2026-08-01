package setupui

import "strings"

// SampleStates is the authoritative state registry for scenedump samples.
func SampleStates() []string {
	return []string{"configure", "progress", "ready", "failure", "lifecycle-progress", "lifecycle-ready", "lifecycle-failure"}
}

// SampleStateUsage returns the state list for CLI help text.
func SampleStateUsage() string {
	return strings.Join(SampleStates(), "|")
}

// SampleFrame composes a deterministic setup or lifecycle sample frame using
// the same production renderer as the live scene.
func SampleFrame(state string, w, h int, pal Palette, sprites map[string]Sprite) string {
	switch state {
	case "lifecycle-progress", "lifecycle-ready", "lifecycle-failure":
		return sampleLifecycleFrame(state[len("lifecycle-"):], w, h, pal, sprites)
	default:
		return sampleSetupFrame(state, w, h, pal, sprites)
	}
}

func sampleSetupFrame(state string, w, h int, pal Palette, sprites map[string]Sprite) string {
	base := [4]Waypoint{
		{Label: "TOOLCHAIN", Landmark: "crate", Meta: []string{"DevPod v0.26.1", "Hauler v2.0.2"}},
		{Label: "RUNTIME", Landmark: "helm", Meta: []string{"docker · docker", "context default"}},
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
		form.SetWidth(min(w-10, 72))
		form.SetCompact(h < 30)
		for i := range base {
			base[i].State = WaypointPending
		}
		data.Waypoints = base
		data.Foreground = form.View()
		data.HelpLine = "tab next · shift+tab prev · enter continue · esc cancel · ctrl+c quit"
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
		data.ReadyLine = "SecondBrain · docker · file backend"
		data.NextCommand = "camp open ~/SecondBrain"
		data.HelpLine = "enter to exit · ctrl+c quit"
	}
	return Compose(data, w, h, pal, sprites)
}
