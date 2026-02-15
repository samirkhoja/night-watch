package main

import "testing"

func TestParseCLIOptionsMaxStepsFlag(t *testing.T) {
	got, err := parseCLIOptions([]string{"--max-steps", "7", "chat"})
	if err != nil {
		t.Fatalf("parseCLIOptions returned error: %v", err)
	}
	if got.MaxSteps != 7 {
		t.Fatalf("unexpected max steps: got=%d", got.MaxSteps)
	}
	if len(got.Args) != 1 || got.Args[0] != "chat" {
		t.Fatalf("unexpected args: %#v", got.Args)
	}
}

func TestParseCLIOptionsMaxStepsEqualsForm(t *testing.T) {
	got, err := parseCLIOptions([]string{"--max-steps=5", "ask", "hello"})
	if err != nil {
		t.Fatalf("parseCLIOptions returned error: %v", err)
	}
	if got.MaxSteps != 5 {
		t.Fatalf("unexpected max steps: got=%d", got.MaxSteps)
	}
}

func TestParseCLIOptionsMaxStepsInvalid(t *testing.T) {
	_, err := parseCLIOptions([]string{"--max-steps", "0"})
	if err == nil {
		t.Fatal("expected validation error for --max-steps=0")
	}
}

func TestParseCLIOptionsRejectsRunbookFlag(t *testing.T) {
	_, err := parseCLIOptions([]string{"--runbook", "./runbooks"})
	if err == nil {
		t.Fatal("expected unknown option error for removed --runbook flag")
	}
}

func TestParseCLIOptionsVersionFlag(t *testing.T) {
	got, err := parseCLIOptions([]string{"--version"})
	if err != nil {
		t.Fatalf("parseCLIOptions returned error: %v", err)
	}
	if !got.ShowVersion {
		t.Fatal("expected ShowVersion=true for --version")
	}
}

func TestParseCLIOptionsAutoApprovalFlag(t *testing.T) {
	got, err := parseCLIOptions([]string{"--auto-approval", "ask", "hello"})
	if err != nil {
		t.Fatalf("parseCLIOptions returned error: %v", err)
	}
	if !got.AutoApproval {
		t.Fatal("expected AutoApproval=true for --auto-approval")
	}
}

func TestParseRunbookInstallOptions(t *testing.T) {
	got, err := parseRunbookInstallOptions([]string{
		"--name", "aws-incidents",
		"--ref=v1.2.3",
		"--subdir", "docs/runbooks",
		"--force",
		"https://github.com/acme/runbooks.git",
	})
	if err != nil {
		t.Fatalf("parseRunbookInstallOptions returned error: %v", err)
	}
	if got.Name != "aws-incidents" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
	if got.Ref != "v1.2.3" {
		t.Fatalf("unexpected ref: %q", got.Ref)
	}
	if got.Subdir != "docs/runbooks" {
		t.Fatalf("unexpected subdir: %q", got.Subdir)
	}
	if !got.Force {
		t.Fatalf("expected force=true")
	}
	if got.Source != "https://github.com/acme/runbooks.git" {
		t.Fatalf("unexpected source: %q", got.Source)
	}
}

func TestParseRunbookInstallOptionsMissingSource(t *testing.T) {
	_, err := parseRunbookInstallOptions([]string{"--name", "foo"})
	if err == nil {
		t.Fatal("expected missing source error")
	}
}
