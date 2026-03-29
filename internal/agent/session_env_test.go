package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/samirkhoja/night-watch/internal/config"
)

func TestCommandEnvVarsUsesAWSProfile(t *testing.T) {
	session := &Session{
		cfg: &config.Config{
			CloudProvider: "aws",
			AWSProfile:    "prod",
		},
	}

	env := session.commandEnvVars()
	if env["AWS_PROFILE"] != "prod" {
		t.Fatalf("expected AWS_PROFILE=prod, got %q", env["AWS_PROFILE"])
	}
	if env["AWS_DEFAULT_PROFILE"] != "prod" {
		t.Fatalf("expected AWS_DEFAULT_PROFILE=prod, got %q", env["AWS_DEFAULT_PROFILE"])
	}
}

func TestCommandEnvVarsSkipsNonAWSCloud(t *testing.T) {
	session := &Session{
		cfg: &config.Config{
			CloudProvider: "gcp",
			AWSProfile:    "prod",
		},
	}

	env := session.commandEnvVars()
	if len(env) != 0 {
		t.Fatalf("expected no env vars for non-aws cloud, got %v", env)
	}
}

func TestRuntimeContextPromptIncludesConfiguredDefaults(t *testing.T) {
	session := &Session{
		workspaceRoot: "/tmp/night-watch-root",
		cfg: &config.Config{
			LLMProvider:     "openai",
			LLMModel:        "gpt-5.4",
			ReasoningEffort: "high",
			CloudProvider:   "aws",
			AWSProfile:      "prod",
		},
	}

	prompt := session.runtimeContextPrompt()
	assertContains := func(want string) {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected runtime prompt to contain %q, got:\n%s", want, prompt)
		}
	}

	assertContains("llm_provider: openai")
	assertContains("llm_model: gpt-5.4")
	assertContains("reasoning_effort: high")
	assertContains("cloud_provider: aws")
	assertContains("aws_profile: prod")
	assertContains("workspace_root: /tmp/night-watch-root")
	assertContains("runbook_root: /tmp/night-watch-root")
	assertContains("Keep normal operations within workspace_root")
	assertContains("use runbook guidance first")
	assertContains("Runbooks are not pre-scanned.")
	assertContains("Use run_command to locate runbook folders/files in runbook_root")
	assertContains("run_command paths and cwd may target workspace_root or runbook_root")
	assertContains("Do not ask the user to restate them")
}

func TestResolvePathInWorkspace(t *testing.T) {
	root := filepath.Clean("/tmp/night-watch-workspace")
	session := &Session{workspaceRoot: root}

	insideRelative, err := session.resolvePathInWorkspace("logs/app")
	if err != nil {
		t.Fatalf("expected inside relative path to resolve, got error: %v", err)
	}
	if !isPathWithinWorkspace(root, insideRelative) {
		t.Fatalf("expected resolved relative path to be within workspace: %q", insideRelative)
	}

	insideAbsolute, err := session.resolvePathInWorkspace(filepath.Join(root, "repo"))
	if err != nil {
		t.Fatalf("expected inside absolute path to resolve, got error: %v", err)
	}
	if !isPathWithinWorkspace(root, insideAbsolute) {
		t.Fatalf("expected resolved absolute path to be within workspace: %q", insideAbsolute)
	}

	if _, err := session.resolvePathInWorkspace("../outside"); err == nil {
		t.Fatalf("expected outside relative traversal to be rejected")
	}

	if _, err := session.resolvePathInWorkspace("/etc"); err == nil {
		t.Fatalf("expected outside absolute path to be rejected")
	}
}

func TestResolveCommandCWDAllowsRunbookOutsideWorkspace(t *testing.T) {
	workspace := filepath.Clean("/tmp/night-watch-workspace")
	runbook := filepath.Clean("/tmp/night-watch-runbooks")
	session := &Session{
		workspaceRoot: workspace,
		runbookRoot:   runbook,
	}

	got, err := session.resolveCommandCWD(runbook)
	if err != nil {
		t.Fatalf("expected runbook cwd to resolve, got error: %v", err)
	}
	if got != runbook {
		t.Fatalf("unexpected cwd: got=%q want=%q", got, runbook)
	}
}

func TestResolveCommandCWDRejectsPathsOutsideAllowedRoots(t *testing.T) {
	workspace := filepath.Clean("/tmp/night-watch-workspace")
	runbook := filepath.Clean("/tmp/night-watch-runbooks")
	session := &Session{
		workspaceRoot: workspace,
		runbookRoot:   runbook,
	}

	if _, err := session.resolveCommandCWD("/tmp"); err == nil {
		t.Fatalf("expected cwd outside workspace and runbook roots to be rejected")
	}
}

func TestEnforceCommandAnchoringRejectsAbsolutePathOutsideRoots(t *testing.T) {
	session := &Session{
		workspaceRoot: filepath.Clean("/tmp/night-watch-workspace"),
		runbookRoot:   filepath.Clean("/tmp/night-watch-runbooks"),
	}
	cwd := filepath.Clean("/tmp/night-watch-workspace")
	if err := session.enforceCommandAnchoring("cat /etc/passwd", cwd); err == nil {
		t.Fatalf("expected anchoring rejection for absolute path outside roots")
	}
}

func TestEnforceCommandAnchoringRejectsRelativeTraversalOutsideWorkspace(t *testing.T) {
	session := &Session{
		workspaceRoot: filepath.Clean("/tmp/night-watch-workspace"),
	}
	cwd := filepath.Clean("/tmp/night-watch-workspace")
	if err := session.enforceCommandAnchoring("cat ../secret.txt", cwd); err == nil {
		t.Fatalf("expected anchoring rejection for relative traversal")
	}
}

func TestEnforceCommandAnchoringAllowsWorkspacePaths(t *testing.T) {
	session := &Session{
		workspaceRoot: filepath.Clean("/tmp/night-watch-workspace"),
	}
	cwd := filepath.Clean("/tmp/night-watch-workspace")
	if err := session.enforceCommandAnchoring("cat ./README.md", cwd); err != nil {
		t.Fatalf("expected workspace command to pass anchoring, got error: %v", err)
	}
}

func TestEnforceCommandAnchoringAllowsRunbookPaths(t *testing.T) {
	workspace := filepath.Clean("/tmp/night-watch-workspace")
	runbook := filepath.Clean("/tmp/night-watch-runbooks")
	session := &Session{
		workspaceRoot: workspace,
		runbookRoot:   runbook,
	}
	if err := session.enforceCommandAnchoring("cat /tmp/night-watch-runbooks/incident.md", workspace); err != nil {
		t.Fatalf("expected runbook path outside workspace to be allowed, got error: %v", err)
	}
}

func TestEnforceCommandAnchoringRejectsOutsideRunbookPath(t *testing.T) {
	workspace := filepath.Clean("/tmp/night-watch-workspace")
	runbook := filepath.Clean("/tmp/night-watch-runbooks")
	session := &Session{
		workspaceRoot: workspace,
		runbookRoot:   runbook,
	}
	if err := session.enforceCommandAnchoring("cat /tmp/other/incident.md", workspace); err == nil {
		t.Fatalf("expected command path outside workspace and runbook to be rejected")
	}
}

func TestEnforceCommandAnchoringChecksInlineOptionPathValues(t *testing.T) {
	session := &Session{
		workspaceRoot: filepath.Clean("/tmp/night-watch-workspace"),
	}
	cwd := filepath.Clean("/tmp/night-watch-workspace")
	if err := session.enforceCommandAnchoring("git --git-dir=/etc/.git status", cwd); err == nil {
		t.Fatalf("expected inline option path outside workspace to be rejected")
	}
}

func TestEnforceCommandAnchoringAllowsNonFilesystemSlashLiterals(t *testing.T) {
	session := &Session{
		workspaceRoot: filepath.Clean("/tmp/night-watch-workspace"),
	}
	cwd := filepath.Clean("/tmp/night-watch-workspace")
	if err := session.enforceCommandAnchoring("aws logs filter-log-events --log-group-name /aws/lambda/night-watch", cwd); err != nil {
		t.Fatalf("expected non-filesystem slash literal to be allowed, got error: %v", err)
	}
}
