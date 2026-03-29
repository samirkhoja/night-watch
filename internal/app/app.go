package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/agent"
	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/providers"
	"github.com/samirkhoja/night-watch/internal/runbooks"
	"github.com/samirkhoja/night-watch/internal/sessionlog"
	"github.com/samirkhoja/night-watch/internal/setup"
	"github.com/samirkhoja/night-watch/internal/ui"
)

type App struct {
	in           io.Reader
	out          io.Writer
	version      string
	autoApproval bool
	workingDir   string
	runbookRoot  string
	maxSteps     int
	cfgManager   *config.Manager
	runbooks     *runbooks.Manager
	sessionLogs  *sessionlog.Manager
	sessionStore agentsdk.SessionStore
	reader       *bufio.Reader
}

type askSession interface {
	Ask(ctx context.Context, prompt string) (string, error)
	SessionID() string
	History() []agentsdk.Message
}

type statefulSession interface {
	LoadState(ctx context.Context) (*agentsdk.SessionState, error)
}

type Options struct {
	ConfigPath   string
	WorkingDir   string
	MaxSteps     int
	Version      string
	AutoApproval bool
}

func New(in io.Reader, out io.Writer, options Options) (*App, error) {
	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workingDir = cwd
	}
	if abs, err := filepath.Abs(workingDir); err == nil {
		workingDir = abs
	}
	maxSteps := options.MaxSteps
	if maxSteps < 0 {
		return nil, errors.New("max steps cannot be negative")
	}

	cfgManager, err := config.NewManager(config.Options{
		CustomConfigPath: options.ConfigPath,
		WorkingDir:       workingDir,
	})
	if err != nil {
		return nil, err
	}
	runbookStore := runbooks.NewManager(cfgManager.ConfigDir())
	if err := runbookStore.Ensure(); err != nil {
		return nil, err
	}
	runbookRoot := runbookStore.StoreDir()

	sessionLogs := sessionlog.NewManager(cfgManager.ConfigDir())
	sessionStore, err := agentsdk.NewFileSessionStore(filepath.Join(cfgManager.ConfigDir(), "sdk-sessions"))
	if err != nil {
		return nil, err
	}
	return &App{
		in:           in,
		out:          out,
		version:      strings.TrimSpace(options.Version),
		autoApproval: options.AutoApproval,
		workingDir:   workingDir,
		runbookRoot:  runbookRoot,
		maxSteps:     maxSteps,
		cfgManager:   cfgManager,
		runbooks:     runbookStore,
		sessionLogs:  sessionLogs,
		sessionStore: sessionStore,
		reader:       bufio.NewReader(in),
	}, nil
}

func (a *App) RunSetup(ctx context.Context) error {
	return setup.Run(
		ctx,
		a.cfgManager,
		a.in,
		a.out,
		setup.DisplayContext{
			WorkspaceRoot: a.workingDir,
			RunbookRoot:   a.runbookRoot,
		},
	)
}

func (a *App) RunAsk(ctx context.Context, prompt string, continueLast bool) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("prompt cannot be empty")
	}
	if a.autoApproval {
		ui.Warn(a.out, "Auto-approval is enabled for this run. Approval prompts will be skipped.")
	}

	cfg, client, compactClient, approval, err := a.loadSessionDeps(ctx)
	if err != nil {
		return err
	}
	session := a.newSession(client, compactClient, cfg, approval)
	session.SetShowUserInput(false)
	if continueLast {
		if err := a.resumeSessionHistory(ctx, session, false); err != nil {
			return err
		}
	}
	return a.runAskWithSession(ctx, cfg, session, prompt)
}

func (a *App) RunChat(ctx context.Context, continueLast bool) error {
	cfg, client, compactClient, approval, err := a.loadSessionDeps(ctx)
	if err != nil {
		return err
	}

	ui.Banner(a.out, a.version)
	ui.ConfigStatus(
		a.out,
		cfg.LLMProvider,
		cfg.LLMModel,
		cfg.ReasoningEffort,
		cfg.CloudProvider,
		cfg.AWSProfile,
		cfg.SlackEnabled,
		a.workingDir,
		a.runbookRoot,
	)
	ui.Status(a.out, "How can I help? Commands: /setup, /reset, /exit")
	if a.autoApproval {
		ui.Warn(a.out, "Auto-approval is enabled for this run. Approval prompts will be skipped.")
	}

	session := a.newSession(client, compactClient, cfg, approval)
	session.SetShowUserInput(false)
	if continueLast {
		if err := a.resumeSessionHistory(ctx, session, true); err != nil {
			return err
		}
	}
	defer func() {
		a.persistSessionHistory(cfg, session)
	}()

	for {
		ui.InputPrompt(a.out)
		raw, err := a.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		input := strings.TrimSpace(raw)
		if input == "" {
			continue
		}

		switch strings.ToLower(input) {
		case "/exit", "exit", "quit":
			ui.Success(a.out, "Goodbye.")
			return nil
		case "/reset":
			session.Reset()
			ui.Success(a.out, "Session context cleared.")
			continue
		case "/setup":
			if err := a.RunSetup(ctx); err != nil {
				ui.Error(a.out, err.Error())
			}
			cfg, client, compactClient, approval, err = a.loadSessionDeps(ctx)
			if err != nil {
				return err
			}
			session = a.newSession(client, compactClient, cfg, approval)
			session.SetShowUserInput(false)
			ui.ConfigStatus(
				a.out,
				cfg.LLMProvider,
				cfg.LLMModel,
				cfg.ReasoningEffort,
				cfg.CloudProvider,
				cfg.AWSProfile,
				cfg.SlackEnabled,
				a.workingDir,
				a.runbookRoot,
			)
			continue
		}

		reply, err := session.Ask(ctx, input)
		if err != nil {
			ui.Error(a.out, err.Error())
			continue
		}
		a.notifySlackRunCompletion(ctx, cfg, input, reply)
	}
}

func (a *App) RunRunbookInstall(
	ctx context.Context,
	source string,
	name string,
	ref string,
	subdir string,
	force bool,
) error {
	record, err := a.runbooks.Install(ctx, runbooks.InstallOptions{
		Source: source,
		Name:   name,
		Ref:    ref,
		Subdir: subdir,
		Force:  force,
	})
	if err != nil {
		return err
	}
	ui.Success(
		a.out,
		fmt.Sprintf("Installed runbook %s (%d markdown file(s)).", record.ID, record.FileCount),
	)
	ui.Status(a.out, "Runbook store: "+a.runbookRoot)
	ui.Status(a.out, "Package path: "+record.PackageDir)
	return nil
}

func (a *App) RunRunbookList(ctx context.Context) error {
	_ = ctx
	items, err := a.runbooks.List()
	if err != nil {
		return err
	}
	ui.Section(a.out, "runbooks")
	if len(items) == 0 {
		ui.Status(a.out, "No runbooks installed.")
		ui.Status(a.out, "Install one with: nwatch runbook install <source>")
		return nil
	}
	for i, item := range items {
		updated := item.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		fmt.Fprintf(
			a.out,
			"  %d) %s  [%d files]  %s\n",
			i+1,
			item.ID,
			item.FileCount,
			updated,
		)
		fmt.Fprintf(a.out, "     name: %s\n", item.Name)
		fmt.Fprintf(a.out, "     src : %s\n", item.Source)
	}
	return nil
}

func (a *App) RunRunbookInspect(ctx context.Context, id string) error {
	_ = ctx
	item, err := a.runbooks.Inspect(id)
	if err != nil {
		return err
	}
	ui.Section(a.out, "runbook")
	fmt.Fprintf(a.out, "id: %s\n", item.ID)
	fmt.Fprintf(a.out, "name: %s\n", item.Name)
	fmt.Fprintf(a.out, "source: %s\n", item.Source)
	if strings.TrimSpace(item.Ref) != "" {
		fmt.Fprintf(a.out, "ref: %s\n", item.Ref)
	}
	if strings.TrimSpace(item.Subdir) != "" {
		fmt.Fprintf(a.out, "subdir: %s\n", item.Subdir)
	}
	fmt.Fprintf(a.out, "files: %d\n", item.FileCount)
	fmt.Fprintf(a.out, "hash: %s\n", item.ContentHash)
	fmt.Fprintf(a.out, "installed: %s\n", item.InstalledAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(a.out, "updated: %s\n", item.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(a.out, "package: %s\n", item.PackageDir)
	if len(item.Files) > 0 {
		fmt.Fprintf(a.out, "markdown files:\n")
		for _, file := range item.Files {
			fmt.Fprintf(a.out, "  - %s\n", file)
		}
	}
	return nil
}

func (a *App) RunRunbookRemove(ctx context.Context, id string) error {
	_ = ctx
	if err := a.runbooks.Remove(id); err != nil {
		return err
	}
	ui.Success(a.out, fmt.Sprintf("Removed runbook %s.", strings.TrimSpace(id)))
	return nil
}

func (a *App) newSession(
	provider agentsdk.Provider,
	compactionProvider agentsdk.Provider,
	cfg config.Config,
	approval *agent.ApprovalManager,
) *agent.Session {
	session := agent.NewSession(provider, &cfg, approval, a.out)
	session.SetCompactionProvider(compactionProvider)
	session.SetSessionStore(a.sessionStore)
	session.SetWorkspaceRoot(a.workingDir)
	session.SetRunbookRoot(a.runbookRoot)
	session.SetMaxSteps(a.maxSteps)
	return session
}

func (a *App) loadSessionDeps(
	ctx context.Context,
) (config.Config, agentsdk.Provider, agentsdk.Provider, *agent.ApprovalManager, error) {
	cfg, err := a.cfgManager.Load(ctx)
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	if !cfg.SetupComplete {
		ui.Warn(a.out, "Setup is not complete. Running setup now.")
		if err := a.RunSetup(ctx); err != nil {
			return config.Config{}, nil, nil, nil, err
		}
		cfg, err = a.cfgManager.Load(ctx)
		if err != nil {
			return config.Config{}, nil, nil, nil, err
		}
	}

	provider, err := providers.New(cfg, a.cfgManager)
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	compactionProvider, _, _ := providers.NewCompaction(cfg, a.cfgManager)

	approval := agent.NewApprovalManager(
		a.in,
		a.reader,
		a.out,
		agent.ApprovalOptions{AutoApprove: a.autoApproval},
	)
	return cfg, provider, compactionProvider, approval, nil
}

func (a *App) persistSessionHistory(cfg config.Config, session *agent.Session) {
	if session == nil {
		return
	}
	a.persistSessionHistorySnapshot(context.Background(), cfg, session)
}

func (a *App) persistSessionHistorySnapshot(
	ctx context.Context,
	cfg config.Config,
	session askSession,
) {
	if session == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var state *agentsdk.SessionState
	if stateful, ok := session.(statefulSession); ok {
		loadedState, err := stateful.LoadState(ctx)
		if err != nil {
			ui.Warn(a.out, "Failed to load session state for snapshot: "+err.Error())
		} else {
			state = loadedState
		}
	}
	meta, err := a.sessionLogs.Save(cfg, session.SessionID(), session.History(), state)
	if err != nil {
		ui.Warn(a.out, "Failed to save session history: "+err.Error())
		return
	}
	if strings.TrimSpace(meta.Path) == "" {
		return
	}
	ui.Status(a.out, "Session saved: "+filepath.Base(meta.Path))
}

func (a *App) runAskWithSession(
	ctx context.Context,
	cfg config.Config,
	session askSession,
	prompt string,
) error {
	if session == nil {
		return errors.New("session is not configured")
	}
	reply, err := session.Ask(ctx, prompt)
	a.persistSessionHistorySnapshot(ctx, cfg, session)
	if err != nil {
		return err
	}
	a.notifySlackRunCompletion(ctx, cfg, prompt, reply)
	return nil
}

func (a *App) resumeSessionHistory(ctx context.Context, session *agent.Session, showResumeReply bool) error {
	if session == nil {
		return nil
	}
	metas, err := a.sessionLogs.List()
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		ui.Warn(a.out, "No previous sessions found. Starting a new session.")
		return nil
	}

	ui.Section(a.out, "resume session")
	limit := len(metas)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		meta := metas[i]
		created := meta.CreatedAt.Local().Format("2006-01-02 15:04:05")
		count := meta.MessageCount
		if count < 0 {
			count = 0
		}
		fmt.Fprintf(
			a.out,
			"  %d) %s  [%d msgs]  %s\n",
			i+1,
			created,
			count,
			meta.Preview,
		)
	}
	fmt.Fprint(a.out, "continue> Select session number (blank for new): ")

	raw, err := a.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	choice := strings.TrimSpace(raw)
	if choice == "" {
		ui.Status(a.out, "Starting a new session.")
		return nil
	}
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > limit {
		ui.Warn(a.out, "Invalid selection. Starting a new session.")
		return nil
	}

	selected := metas[index-1]
	snapshot, err := a.sessionLogs.LoadSnapshot(selected.Path)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(snapshot.SessionID)
	if sessionID == "" {
		sessionID = selected.ID
	}
	session.SetSessionID(sessionID)
	restored, err := session.LoadHistoryFromStore(ctx)
	if err != nil {
		return err
	}
	if !restored {
		if err := session.RestoreState(ctx, snapshot.State); err != nil {
			return err
		}
	}
	if _, _, err := session.ResumeActiveRun(ctx, showResumeReply); err != nil {
		return err
	}
	ui.Success(a.out, fmt.Sprintf("Loaded session %s (%d messages).", selected.ID, len(session.History())))
	return nil
}
