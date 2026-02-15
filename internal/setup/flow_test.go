package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAWSProfileNamesFromINI(t *testing.T) {
	raw := `
[default]
aws_access_key_id = abc

[profile prod]
region = us-east-1

[dev]
region = us-west-2
`

	got := parseAWSProfileNamesFromINI(raw)
	want := []string{"default", "dev", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected profiles: got=%v want=%v", got, want)
	}
}

func TestSelectYesNo(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		current bool
		want    bool
		wantErr bool
	}{
		{name: "yes", input: "y\n", current: false, want: true},
		{name: "no", input: "n\n", current: true, want: false},
		{name: "default yes", input: "\n", current: true, want: true},
		{name: "default no", input: "\n", current: false, want: false},
		{name: "invalid", input: "maybe\n", current: false, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(bytes.NewBufferString(tc.input))
			out := bytes.NewBuffer(nil)
			got, err := selectYesNo(reader, out, "Enable Slack notifications", tc.current)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("selectYesNo returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected choice: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestValidateSlackWebhookURL(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{input: "https://hooks.slack.com/services/T000/B000/XXX", wantErr: false},
		{input: "http://localhost/test", wantErr: false},
		{input: "ftp://hooks.slack.com/services/T000/B000/XXX", wantErr: true},
		{input: "not-a-url", wantErr: true},
	}
	for _, tc := range tests {
		err := validateSlackWebhookURL(tc.input)
		if tc.wantErr && err == nil {
			t.Fatalf("expected error for %q", tc.input)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected error for %q: %v", tc.input, err)
		}
	}
}

func TestCheckCloudCredentialsAWSConfiguredAndConfirmed(t *testing.T) {
	restore := overrideSetupCommandFuncs(
		func(name string) (string, error) {
			if name == "aws" {
				return "/usr/bin/aws", nil
			}
			return "", errors.New("not found")
		},
		func(ctx context.Context, name string, args ...string) (string, string, error) {
			_ = ctx
			if name != "aws" {
				return "", "", errors.New("unexpected command")
			}
			return `{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/test"}`, "", nil
		},
	)
	defer restore()

	reader := bufio.NewReader(bytes.NewBufferString("y\n"))
	out := bytes.NewBuffer(nil)
	err := checkCloudCredentials(context.Background(), reader, out, "aws", "prod")
	if err != nil {
		t.Fatalf("checkCloudCredentials returned error: %v", err)
	}
	if !strings.Contains(out.String(), "AWS CLI is configured and authenticated") {
		t.Fatalf("expected success output, got:\n%s", out.String())
	}
}

func TestCheckCloudCredentialsAWSMissingCLI(t *testing.T) {
	restore := overrideSetupCommandFuncs(
		func(name string) (string, error) {
			if name == "aws" {
				return "", errors.New("not found")
			}
			return "/bin/true", nil
		},
		func(ctx context.Context, name string, args ...string) (string, string, error) {
			_ = ctx
			return "", "", nil
		},
	)
	defer restore()

	reader := bufio.NewReader(bytes.NewBufferString("y\n"))
	out := bytes.NewBuffer(nil)
	err := checkCloudCredentials(context.Background(), reader, out, "aws", "prod")
	if err == nil {
		t.Fatal("expected error when AWS CLI is missing")
	}
	if !strings.Contains(err.Error(), "configure AWS CLI auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckCloudCredentialsRejectsWhenUserDoesNotConfirm(t *testing.T) {
	restore := overrideSetupCommandFuncs(
		func(name string) (string, error) {
			if name == "aws" {
				return "/usr/bin/aws", nil
			}
			return "", errors.New("not found")
		},
		func(ctx context.Context, name string, args ...string) (string, string, error) {
			_ = ctx
			if name != "aws" {
				return "", "", errors.New("unexpected command")
			}
			return `{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/test"}`, "", nil
		},
	)
	defer restore()

	reader := bufio.NewReader(bytes.NewBufferString("n\n"))
	out := bytes.NewBuffer(nil)
	err := checkCloudCredentials(context.Background(), reader, out, "aws", "prod")
	if err == nil {
		t.Fatal("expected user confirmation rejection error")
	}
	if !strings.Contains(err.Error(), "setup canceled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunProviderCLICommandTimesOut(t *testing.T) {
	originalRun := runExternalCommand
	originalTimeout := providerCheckTimeout
	t.Cleanup(func() {
		runExternalCommand = originalRun
		providerCheckTimeout = originalTimeout
	})

	providerCheckTimeout = 5 * time.Millisecond
	runExternalCommand = func(ctx context.Context, name string, args ...string) (string, string, error) {
		_ = name
		_ = args
		<-ctx.Done()
		return "", "", ctx.Err()
	}

	_, _, err := runProviderCLICommand(context.Background(), "aws", "sts", "get-caller-identity")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckCloudCredentialsMultiUnsupported(t *testing.T) {
	reader := bufio.NewReader(bytes.NewBufferString("y\n"))
	out := bytes.NewBuffer(nil)
	err := checkCloudCredentials(context.Background(), reader, out, "multi", "default")
	if err == nil {
		t.Fatal("expected multi provider to be rejected")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func overrideSetupCommandFuncs(
	lookup func(string) (string, error),
	run func(context.Context, string, ...string) (string, string, error),
) func() {
	originalLookup := lookupBinary
	originalRun := runExternalCommand
	lookupBinary = lookup
	runExternalCommand = run
	return func() {
		lookupBinary = originalLookup
		runExternalCommand = originalRun
	}
}
