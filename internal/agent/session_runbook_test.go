package agent

import "testing"

func TestSetRunbookRootAllowsPathOutsideWorkspace(t *testing.T) {
	session := &Session{
		workspaceRoot: "/tmp/night-watch/workspace",
		runbookRoot:   "/tmp/night-watch/workspace",
	}

	session.SetRunbookRoot("/opt/shared-runbooks")

	if session.runbookRoot != "/opt/shared-runbooks" {
		t.Fatalf("expected external runbook root to be preserved, got %q", session.runbookRoot)
	}
}

func TestSetWorkspaceRootDoesNotOverrideExternalRunbookRoot(t *testing.T) {
	session := &Session{
		workspaceRoot: "/tmp/night-watch/workspace",
		runbookRoot:   "/opt/shared-runbooks",
	}

	session.SetWorkspaceRoot("/tmp/night-watch/other-workspace")

	if session.workspaceRoot != "/tmp/night-watch/other-workspace" {
		t.Fatalf("unexpected workspace root: %q", session.workspaceRoot)
	}
	if session.runbookRoot != "/opt/shared-runbooks" {
		t.Fatalf("external runbook root should remain unchanged, got %q", session.runbookRoot)
	}
}
