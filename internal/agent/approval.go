package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/samirkhoja/night-watch/internal/ui"
)

type ApprovalDecision string

const (
	AllowOnce    ApprovalDecision = "allow_once"
	AllowAlways  ApprovalDecision = "allow_always"
	RejectOnce   ApprovalDecision = "reject_once"
	RejectAlways ApprovalDecision = "reject_always"
)

type ApprovalManager struct {
	reader          *bufio.Reader
	out             io.Writer
	deadlineReader  readDeadlineSetter
	sessionPolicies map[string]string
	mu              sync.Mutex
	autoApprove     bool
}

type readDeadlineSetter interface {
	SetReadDeadline(t time.Time) error
}

type ApprovalOptions struct {
	AutoApprove bool
}

func NewApprovalManager(
	input io.Reader,
	reader *bufio.Reader,
	out io.Writer,
	options ...ApprovalOptions,
) *ApprovalManager {
	var deadlineReader readDeadlineSetter
	if typed, ok := input.(readDeadlineSetter); ok {
		deadlineReader = typed
	}
	autoApprove := false
	if len(options) > 0 {
		autoApprove = options[0].AutoApprove
	}

	return &ApprovalManager{
		reader:          reader,
		out:             out,
		deadlineReader:  deadlineReader,
		sessionPolicies: map[string]string{},
		autoApprove:     autoApprove,
	}
}

func (a *ApprovalManager) AutoApproveEnabled() bool {
	return a != nil && a.autoApprove
}

func (a *ApprovalManager) Request(
	ctx context.Context,
	command string,
	cwd string,
	timeoutSeconds int,
) (bool, error) {
	if a == nil {
		return false, errors.New("approval manager is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoApprove {
		return true, nil
	}

	key := policyKey(cwd, command)
	if val := strings.TrimSpace(a.sessionPolicies[key]); val != "" {
		return val == "allow", nil
	}

	ui.ApprovalBox(a.out, command, cwd, timeoutSeconds)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		fmt.Fprint(a.out, "approval> ")
		raw, err := a.readLine(ctx)
		if err != nil {
			return false, err
		}
		ui.ClearInputLine(a.out)

		if idx, ok := parseNumericChoice(raw, 4); ok {
			return decisionForIndex(a, key, idx)
		}

		decision := parseDecision(raw)
		switch decision {
		case AllowOnce:
			return true, nil
		case RejectOnce:
			return false, nil
		case AllowAlways:
			a.sessionPolicies[key] = "allow"
			return true, nil
		case RejectAlways:
			a.sessionPolicies[key] = "reject"
			return false, nil
		default:
			ui.Warn(a.out, "Use 1-4, or: allow | always allow | reject | always reject")
		}
	}
}

func (a *ApprovalManager) readLine(ctx context.Context) (string, error) {
	if a == nil || a.reader == nil {
		return "", io.EOF
	}
	if a.deadlineReader == nil {
		return a.reader.ReadString('\n')
	}
	return a.readLineWithDeadline(ctx)
}

func (a *ApprovalManager) readLineWithDeadline(ctx context.Context) (string, error) {
	const pollInterval = 200 * time.Millisecond
	var builder strings.Builder

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		if err := a.deadlineReader.SetReadDeadline(time.Now().Add(pollInterval)); err != nil {
			// If deadlines are unsupported, fall back to a blocking read.
			return a.reader.ReadString('\n')
		}

		chunk, err := a.reader.ReadString('\n')
		if chunk != "" {
			builder.WriteString(chunk)
		}
		if err == nil {
			_ = a.deadlineReader.SetReadDeadline(time.Time{})
			return builder.String(), nil
		}
		if isTimeoutError(err) {
			// Timeout is expected here; it gives us periodic chances to observe ctx cancellation.
			continue
		}
		_ = a.deadlineReader.SetReadDeadline(time.Time{})
		if errors.Is(err, io.EOF) && builder.Len() > 0 {
			return builder.String(), nil
		}
		return "", err
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsTimeout(err) {
		return true
	}
	type timeout interface {
		Timeout() bool
	}
	if typed, ok := err.(timeout); ok {
		return typed.Timeout()
	}
	return false
}

func decisionForIndex(a *ApprovalManager, key string, idx int) (bool, error) {
	switch idx {
	case 0:
		return true, nil
	case 1:
		a.sessionPolicies[key] = "allow"
		return true, nil
	case 2:
		return false, nil
	case 3:
		a.sessionPolicies[key] = "reject"
		return false, nil
	default:
		return false, fmt.Errorf("invalid approval selection")
	}
}

func parseDecision(input string) ApprovalDecision {
	normalized := strings.ToLower(strings.TrimSpace(input))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")

	switch normalized {
	case "allow", "a", "yes", "y":
		return AllowOnce
	case "always allow", "allow always", "aa":
		return AllowAlways
	case "reject", "r", "no", "n":
		return RejectOnce
	case "always reject", "reject always", "ar":
		return RejectAlways
	default:
		return ""
	}
}

func policyKey(cwd string, command string) string {
	name := commandPolicyName(command)
	if name == "" {
		name = "unknown"
	}
	return "cmd::" + name
}

func commandPolicyName(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	firstLine := command
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return ""
	}

	tokens := strings.Fields(firstLine)
	if len(tokens) == 0 {
		return ""
	}

	index := 0
	for index < len(tokens) && isEnvAssignmentToken(tokens[index]) {
		index++
	}
	if index >= len(tokens) {
		return ""
	}

	token := tokens[index]
	for {
		// Normalize wrappers so policy decisions are keyed by the underlying executable.
		lower := strings.ToLower(strings.TrimSpace(token))
		switch lower {
		case "sudo", "command", "builtin":
			index++
			if index >= len(tokens) {
				return ""
			}
			token = tokens[index]
			continue
		case "env":
			index++
			for index < len(tokens) && isEnvAssignmentToken(tokens[index]) {
				index++
			}
			if index >= len(tokens) {
				return ""
			}
			token = tokens[index]
			continue
		}
		break
	}

	token = strings.Trim(token, `"'`)
	if cut := strings.LastIndex(token, "/"); cut >= 0 {
		token = token[cut+1:]
	}
	token = strings.ToLower(strings.TrimSpace(token))
	return token
}

func isEnvAssignmentToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	key := token[:eq]
	for i, r := range key {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

func parseNumericChoice(input string, max int) (int, bool) {
	value := strings.TrimSpace(input)
	if value == "" {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > max {
		return 0, false
	}
	return n - 1, true
}
