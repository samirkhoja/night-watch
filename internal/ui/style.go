package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	reset         = "\033[0m"
	bold          = "\033[1m"
	dim           = "\033[2m"
	cyan          = "\033[36m"
	cyanBright    = "\033[96m"
	green         = "\033[32m"
	yellow        = "\033[33m"
	yellowBright  = "\033[93m"
	red           = "\033[31m"
	magentaBright = "\033[95m"
	gray          = "\033[90m"
)

var renderMu sync.Mutex

func Banner(out io.Writer, version string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	line := strings.Repeat("=", 48)
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	fmt.Fprintf(out, "%s%sNight Watch%s %sv%s%s\n", bold, cyanBright, reset, gray, version, reset)
	fmt.Fprintf(out, "%s%s%s\n\n", gray, line, reset)
}

func Section(out io.Writer, title string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s%s%s\n", bold, cyan, strings.ToUpper(title))
	fmt.Fprintf(out, "%s%s%s\n", gray, strings.Repeat("-", max(8, len(title))), reset)
}

func Status(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s• %s%s\n", cyan, msg, reset)
}

func Thinking(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s• %s%s\n", gray, msg, reset)
}

func Reason(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	if strings.TrimSpace(msg) == "" {
		return
	}
	fmt.Fprintf(out, "%s• %s%sreason:%s %s%s%s\n", gray, bold, yellowBright, reset, yellowBright, msg, reset)
}

func Success(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s✓ %s%s\n", green, msg, reset)
}

func Warn(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s! %s%s\n", yellow, msg, reset)
}

func Error(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s✗ %s%s\n", red, msg, reset)
}

func User(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s> %s%s\n", magentaBright, msg, reset)
}

func InputPrompt(out io.Writer) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "\n%s>%s ", magentaBright, reset)
}

func Assistant(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s%s%s\n", cyanBright, msg, reset)
}

func Reasoning(out io.Writer, msg string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	if strings.TrimSpace(msg) == "" {
		return
	}
	fmt.Fprintf(out, "%sreasoning:%s %s%s\n", dim, reset, gray, msg)
	fmt.Fprint(out, reset)
}

func ConfigStatus(
	out io.Writer,
	llmProvider,
	llmModel,
	reasoningEffort,
	cloudProvider,
	awsProfile string,
	slackEnabled bool,
	workspaceRoot,
	runbookRoot string,
) {
	renderMu.Lock()
	defer renderMu.Unlock()
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = "medium"
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	fmt.Fprintf(out, "%s%s%s\n", bold, cyan, strings.ToUpper("configuration"))
	fmt.Fprintf(out, "%s%s%s\n", gray, strings.Repeat("-", max(8, len("configuration"))), reset)
	fmt.Fprintf(
		out,
		"%sProvider:%s %s%s%s  %sModel:%s %s%s%s  %sReasoning:%s %s%s%s  %sCloud:%s %s%s%s\n",
		gray,
		reset,
		cyan,
		llmProvider,
		reset,
		gray,
		reset,
		cyan,
		llmModel,
		reset,
		gray,
		reset,
		cyan,
		reasoningEffort,
		reset,
		gray,
		reset,
		cyan,
		cloudProvider,
		reset,
	)
	if strings.TrimSpace(awsProfile) != "" && cloudProvider == "aws" {
		fmt.Fprintf(out, "%sProfile:%s %s%s%s\n", gray, reset, cyan, awsProfile, reset)
	}
	if slackEnabled {
		fmt.Fprintf(out, "%sSlack Notifications:%s %senabled%s\n", gray, reset, green, reset)
	} else {
		fmt.Fprintf(out, "%sSlack Notifications:%s %sdisabled%s\n", gray, reset, gray, reset)
	}
	if workspaceRoot != "" {
		fmt.Fprintf(out, "%sWorkspace:%s %s%s%s\n", gray, reset, cyan, workspaceRoot, reset)
	}
	runbookRoot = strings.TrimSpace(runbookRoot)
	if runbookRoot != "" {
		fmt.Fprintf(out, "%sRunbook Store:%s %s%s%s\n", gray, reset, cyan, runbookRoot, reset)
	}
	fmt.Fprint(out, "\n")
}

func ApprovalBox(out io.Writer, command, cwd string, timeoutSeconds int) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprintf(out, "%s%s%s\n", bold, cyan, strings.ToUpper("command approval required"))
	fmt.Fprintf(out, "%s%s%s\n", gray, strings.Repeat("-", max(8, len("command approval required"))), reset)
	command = strings.TrimSpace(command)
	if strings.Contains(command, "\n") {
		fmt.Fprintf(out, "%sCommand:%s\n", gray, reset)
		for _, line := range strings.Split(command, "\n") {
			fmt.Fprintf(out, "  %s%s%s\n", magentaBright, line, reset)
		}
	} else {
		fmt.Fprintf(out, "%sCommand:%s %s%s%s\n", gray, reset, magentaBright, command, reset)
	}
	fmt.Fprintf(out, "%sCWD:%s %s\n", gray, reset, cwd)
	fmt.Fprintf(out, "%sTimeout:%s %ds\n", gray, reset, timeoutSeconds)
	fmt.Fprintf(out, "\n%sChoices:%s\n", gray, reset)
	fmt.Fprintf(out, "  %s1)%s %sallow%s          run once\n", gray, reset, green, reset)
	fmt.Fprintf(out, "  %s2)%s %salways allow%s   allow for this session\n", gray, reset, green, reset)
	fmt.Fprintf(out, "  %s3)%s %sreject%s         deny once\n", gray, reset, red, reset)
	fmt.Fprintf(out, "  %s4)%s %salways reject%s  deny for this session\n", gray, reset, red, reset)
	fmt.Fprintf(out, "%sUse number (1-4) or text.%s\n\n", dim, reset)
}

func RunningCommand(out io.Writer, command string) {
	renderMu.Lock()
	defer renderMu.Unlock()
	command = strings.TrimSpace(command)
	if command == "" {
		fmt.Fprintf(out, "%s• %s%s\n", cyan, "running command", reset)
		return
	}
	if strings.Contains(command, "\n") {
		fmt.Fprintf(out, "%s• running command:%s\n", cyan, reset)
		for _, line := range strings.Split(command, "\n") {
			fmt.Fprintf(out, "  %s%s%s\n", magentaBright, line, reset)
		}
		return
	}
	fmt.Fprintf(out, "%s• running command:%s %s%s%s\n", cyan, reset, magentaBright, command, reset)
}

func ClearInputLine(out io.Writer) {
	renderMu.Lock()
	defer renderMu.Unlock()
	fmt.Fprint(out, "\r\033[K")
}
