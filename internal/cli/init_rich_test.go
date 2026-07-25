package cli

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/setupui"
)

func TestRichInitPipelineRelaysRealInitStages(t *testing.T) {
	run := func(ctx context.Context, request InitRequest, _ OutputMode, _ io.Writer) error {
		if request.Capsule != "alpha" {
			t.Fatalf("request name = %q", request.Capsule)
		}
		for _, message := range []string{"Writing camp manifest…", "Camp manifest written.", "Initializing capsule…", "Capsule initialized."} {
			if err := reportInitActivity(ctx, message); err != nil {
				return err
			}
		}
		return nil
	}
	pipeline := newRichInitPipeline(context.Background(), InitRequest{Root: "/work/alpha"}, run)
	var got []string
	for message := range pipeline.Start(map[string]string{"name": "alpha"}) {
		switch message := message.(type) {
		case setupui.ConfigAcceptedMsg:
			got = append(got, "accepted")
		case setupui.ActivityMsg:
			got = append(got, "activity:"+message.Message)
		case setupui.WaypointCompletedMsg:
			got = append(got, "complete:"+initStageName(message.Stage))
		case setupui.AllReadyMsg:
			got = append(got, "ready")
		default:
			t.Fatalf("unexpected message %T", message)
		}
	}
	want := []string{
		"accepted",
		"activity:Writing camp manifest…",
		"complete:manifest",
		"activity:Initializing capsule…",
		"complete:capsule",
		"complete:runtime",
		"complete:ready",
		"ready",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	select {
	case <-pipeline.Done():
	default:
		t.Fatal("pipeline did not close Done")
	}
}

func TestRichInitPipelineClosesWhenCanceledBeforeSubmission(t *testing.T) {
	pipeline := newRichInitPipeline(context.Background(), InitRequest{}, func(context.Context, InitRequest, OutputMode, io.Writer) error {
		t.Fatal("init should not run before submission")
		return nil
	})
	pipeline.markDoneIfNotStarted()
	select {
	case <-pipeline.Done():
	default:
		t.Fatal("pre-submit cancellation did not close Done")
	}
}

func initStageName(stage setupui.Stage) string {
	return map[setupui.Stage]string{
		setupui.StageToolchain: "manifest",
		setupui.StageRuntime:   "capsule",
		setupui.StageCapsule:   "runtime",
		setupui.StageStorage:   "ready",
	}[stage]
}
