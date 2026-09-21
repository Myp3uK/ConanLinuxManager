package systemd

import (
	"context"
	"testing"
	"time"
)

func TestShowReadsAnAdoptedUnit(t *testing.T) {
	run := func(ctx context.Context, args ...string) (string, error) {
		return "LoadState=loaded\nActiveState=active\nResult=success\nMainPID=370170\n" +
			"ExecMainCode=0\nExecMainStatus=0\nExecMainStartTimestamp=@1789986021\n", nil
	}
	s, err := Show(context.Background(), run, "conan-server@ue5-test.service")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running() || s.MainPID != 370170 || !s.StartedAt.Equal(time.Unix(1789986021, 0)) {
		t.Fatalf("%+v", s)
	}
}

func TestShowToleratesAUnitThatNeverStarted(t *testing.T) {
	run := func(ctx context.Context, args ...string) (string, error) {
		return "LoadState=loaded\nActiveState=inactive\nResult=success\nMainPID=0\nExecMainStartTimestamp=\n", nil
	}
	s, err := Show(context.Background(), run, "conan-server@new.service")
	if err != nil || !s.StartedAt.IsZero() {
		t.Fatalf("%+v %v", s, err)
	}
}
