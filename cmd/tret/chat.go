package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/triskill/tret/internal/chat"
	"github.com/triskill/tret/internal/chatflow"
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/journal"
	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"github.com/triskill/tret/pkg/worker"
)

func runChat(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := executeChat(ctx, args, os.Stdin, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "chat: %q\n", err.Error())
		os.Exit(1)
	}
}

func executeChat(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	cfg, err := config.FromChatFlags(args)
	if err != nil {
		return err
	}
	profiles, err := modelProfiles(cfg)
	if err != nil {
		return err
	}
	if err := checkLease(cfg.JournalLease.Duration(), map[string]time.Duration{"chat": llm.MaxCallDuration}); err != nil {
		return err
	}
	if cfg.LockDuration.Duration() <= llm.MaxCallDuration {
		return fmt.Errorf("lock duration must outlast %s", llm.MaxCallDuration)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := journal.NewFile(cfg.JournalDir)
	if err != nil {
		return err
	}
	// Session-specific subscriptions keep other workers from executing this
	// chat's commands in a different directory or with different credentials.
	id := fmt.Sprintf("%s-chat-%d", processClientID(), time.Now().UnixNano())
	llmType, execType := "llm-call-v1-"+id, "exec-command-v1-"+id
	guard := journal.NewGuard(store, id, cfg.JournalLease.Duration())
	model := llm.NewWorker(profiles, guard)
	shell, err := shellexec.NewWorker(cwd, guard)
	if err != nil {
		return err
	}
	registry := worker.NewRegistry()
	for typ, handler := range map[string]zen.WorkerFunc{llmType: model.Handle, execType: shell.Handle} {
		if err := registry.Register(worker.Spec{JobType: typ, LockDuration: cfg.LockDuration.Duration(), MaxActiveJobs: 1, MaxRuntime: llm.MaxCallDuration}, handler); err != nil {
			return err
		}
	}
	// Keep routine worker logs out of the terminal conversation.
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previousLogger)
	client := zen.NewClient(strings.TrimRight(cfg.EngineREST, "/"))
	bpmn := strings.ReplaceAll(string(chatflow.BPMN), "llm-call-v1", llmType)
	bpmn = strings.ReplaceAll(bpmn, "exec-command-v1", execType)
	bpmn = strings.ReplaceAll(bpmn, "tret_chat", "tret_chat_"+strings.ReplaceAll(id, "-", "_"))
	definition, _, err := client.Deploy([]byte(bpmn))
	if err != nil {
		return fmt.Errorf("deploy chat (start the engine with make up): %w", err)
	}
	conn, err := zen.DialGRPC(cfg.EngineGRPC)
	if err != nil {
		return err
	}
	defer conn.Close()
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	if _, err := zen.NewGrpcClient(conn).RegisterWorkerWithOptions(workerCtx, id, registry.Handle, registry.Options()...); err != nil {
		return err
	}
	instance, err := client.StartInstance(definition, id, map[string]any{
		"request": map[string]any{}, "response": nil, "execution": nil,
		"turn": 0, "max_turns": 8, "quit": false,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "tret — %q\nCommands require approval and run with your permissions (no sandbox).\n/exit to quit. Session %d\n", cwd, instance)
	if err == nil {
		err = chat.Run(ctx, client, instance, input, output)
	}
	// Stop receiving new work before canceling the instance and draining
	// handlers that were already admitted. They journal their results even
	// after the worker stream closes.
	registry.StopAccepting()
	stopWorkers()
	if err != nil {
		if cancelErr := client.CancelInstance(instance); cancelErr != nil {
			err = fmt.Errorf("%v; cancel session %d: %w", err, instance, cancelErr)
		} else if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	// A command already approved may still be running after Ctrl-C. Let the
	// journal record its outcome before closing the worker connection.
	drainCtx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
	defer cancel()
	if drainErr := registry.Drain(drainCtx); drainErr != nil {
		err = errors.Join(err, fmt.Errorf("drain chat workers: %w", drainErr))
	}
	return err
}
