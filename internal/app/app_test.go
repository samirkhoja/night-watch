package app

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestNewRejectsNegativeMaxSteps(t *testing.T) {
	_, err := New(bytes.NewBuffer(nil), bytes.NewBuffer(nil), Options{
		WorkingDir: filepath.Clean("/tmp/night-watch/workspace"),
		MaxSteps:   -1,
	})
	if err == nil {
		t.Fatal("expected error for negative max steps")
	}
}

func TestNewUsesConfigRunbookStore(t *testing.T) {
	configDir := filepath.Clean("/tmp/night-watch-config")
	t.Setenv("NIGHTWATCH_CONFIG_DIR", configDir)

	application, err := New(bytes.NewBuffer(nil), bytes.NewBuffer(nil), Options{
		WorkingDir: filepath.Clean("/tmp/night-watch/workspace"),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	wantRunbookRoot := filepath.Join(configDir, "runbooks-installed")
	if application.runbookRoot != wantRunbookRoot {
		t.Fatalf("unexpected runbook root: got=%q want=%q", application.runbookRoot, wantRunbookRoot)
	}
}
