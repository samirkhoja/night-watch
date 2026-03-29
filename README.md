# Night Watch

Night Watch is a DevOps agent that uses an LLM agent to run diagnostics, inspect cloud/log signals, and correlate issues with code changes.

## Experimental Disclaimer

This project is experimental. LLMs can perform unexpected behavior. 

Always validate before making production-impacting decisions.

## Requirements

- Go `1.23+`
- One LLM provider API key: `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` or `GOOGLE_API_KEY`
- Optional cloud/provider CLIs depending on your environment: `aws`, `gcloud`, `sentry-cli`

## Quick Start

1. Run setup:

```bash
go run ./cmd/nwatch setup
```

2. Start chat:

```bash
go run ./cmd/nwatch
```

3. Ask a one-shot question:

```bash
go run ./cmd/nwatch ask "check recent errors and correlate with commits"
```

## Install Globally

From this repository:

```bash
go install ./cmd/nwatch
```

From module path:

```bash
go install github.com/samirkhoja/night-watch/cmd/nwatch@latest
```

## Usage

Installed binary:

```bash
nwatch [flags]
nwatch [flags] setup
nwatch [flags] chat
nwatch [flags] ask <prompt>
nwatch [flags] runbook <command>
nwatch [flags] help
```

Runtime commands in chat:

- `/setup` rerun setup
- `/reset` clear current session context
- `/exit` exit chat

Continue a prior session:

```bash
nwatch --continue
```

Set an optional hard cap for parent-agent steps (omit for unlimited):

```bash
nwatch --max-steps 12
```

## Setup Flow

The interactive setup prompts for:

- LLM provider (`openai`, `anthropic`, `google`)
- Model name
- Reasoning effort (`low`, `medium`, `high`)
- Cloud provider (`aws`, `gcp`, `sentry`)
- AWS profile (for `aws`)
- Cloud CLI auth verification for selected provider(s), then confirmation of detected environment before setup continues
- Slack notifications (`enabled` / `disabled`)
- Slack webhook URL (`SLACK_WEBHOOK_URL`) when Slack is enabled
- Missing provider API key (saved into `.env`)

## CLI Options

- `-c, --config <file>`: optional custom settings JSON (highest precedence)
- `--max-steps <n>`: optional hard cap for parent-agent steps (`n >= 1`; omit for unlimited)
- `--continue`: select and continue from a previous saved session
- `--auto-approval`: skip command approval prompts for this process
- `-v, --version`: print CLI version
- `-h, --help`: print help

Supported forms:

- `--config /path/to/file.json`
- `--config=/path/to/file.json`
- `--max-steps 12`
- `--max-steps=12`
- `--auto-approval`

## Runbook Install

Runbooks are managed and stored by the CLI, then discovered by the agent via CLI commands (`ls`, `find`, `rg`, `cat`) when needed.

Install from a local markdown file or directory:

```bash
nwatch runbook install ./runbooks
nwatch runbook install ./runbooks/aws/incident.md --name aws-incidents
```

Install from git:

```bash
nwatch runbook install https://github.com/acme/runbooks.git
nwatch runbook install https://github.com/acme/runbooks.git --ref v1.2.0 --subdir docs/incidents
```

Manage installed runbooks:

```bash
nwatch runbook list
nwatch runbook inspect aws-incidents
nwatch runbook remove aws-incidents
```

## Configuration

Settings are layered in this order:

1. `~/.config/night-watch/config.json`
2. `.nightwatch/settings.json` (nearest parent)
3. `.nightwatch/settings.local.json` (nearest parent)
4. `--config <file>` or `NIGHTWATCH_CONFIG_FILE` (highest precedence)

Config dir override:

```bash
export NIGHTWATCH_CONFIG_DIR=/path/to/config
```

Provider keys are read from environment first, then:

- `~/.config/night-watch/.env`

Slack webhook is read from environment first, then:

- `SLACK_WEBHOOK_URL` in `~/.config/night-watch/.env`

Example config:

```json
{
  "setup_complete": true,
  "llm_provider": "openai",
  "llm_model": "gpt-5.4",
  "reasoning_effort": "medium",
  "cloud_provider": "aws",
  "aws_profile": "default",
  "slack_enabled": true
}
```

When `slack_enabled` is true and `SLACK_WEBHOOK_URL` is configured, Night Watch sends a Slack notification after each successful agent run.

## Command Approval

Most tool-executed commands require approval. Choices are:

- `allow` (run once)
- `always allow` (allow for this CLI session)
- `reject` (deny once)
- `always reject` (deny for this CLI session)

Selection input:

- number (`1-4`)
- text (`allow`, `always allow`, `reject`, `always reject`)

Notes:

- `always allow` and `always reject` are session-scoped only.
- Session policy is tracked per command executable name.
- Low-risk commands auto-approved by default: `ls`, `pwd`, `whoami`, `date`, `which`
- With `--auto-approval`, approval prompts are skipped for the entire CLI run.
- Hard safety blocks still apply.
- Commands that look potentially destructive are labeled as dangerous when auto-approved.

## Workspace and Runbook Anchors

- `workspace_root` is where command working directories are anchored.
- `runbook_root` is the managed runbook store used by `nwatch runbook install`.
- Installed runbooks are stored under `~/.config/night-watch/runbooks-installed` (or `NIGHTWATCH_CONFIG_DIR/runbooks-installed`).
- The agent searches `runbook_root` first for incident runbook markdown/folders.

## Sessions

- Session logs are saved on exit to `~/.config/night-watch/sessions/session-YYYYMMDD-HHMMSS.md`
- `--continue` shows recent sessions and loads one into context.

## Test

```bash
GOCACHE=/tmp/gocache go test ./...
```
