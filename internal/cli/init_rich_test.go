package cli

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

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
	runCalls := 0
	pipeline := newRichInitPipeline(context.Background(), InitRequest{}, func(context.Context, InitRequest, OutputMode, io.Writer) error {
		runCalls++
		return nil
	})
	pipeline.markDoneIfNotStarted()
	select {
	case <-pipeline.Done():
	default:
		t.Fatal("pre-submit cancellation did not close Done")
	}
	for range pipeline.Start(map[string]string{"name": "late"}) {
	}
	if runCalls != 0 {
		t.Fatalf("init ran %d time(s) after pre-submit cancellation", runCalls)
	}
}

func TestRichInitPipelineRepeatedStartStreamsTerminate(t *testing.T) {
	pipeline := newRichInitPipeline(context.Background(), InitRequest{}, func(context.Context, InitRequest, OutputMode, io.Writer) error {
		return nil
	})
	first := pipeline.Start(map[string]string{"name": "alpha"})
	second := pipeline.Start(map[string]string{"name": "alpha"})
	for range first {
	}
	select {
	case _, ok := <-second:
		if ok {
			t.Fatal("repeated Start returned an additional message stream")
		}
	case <-time.After(time.Second):
		t.Fatal("repeated Start stream did not terminate")
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
