package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Shooa/wln/internal/app"
	"github.com/Shooa/wln/internal/selfupdate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agentMode := isAgentInvocation(os.Args[0])
	if !agentMode && offerUpdate(ctx, os.Args[1:]) {
		return
	}

	run := app.Run
	if agentMode {
		run = app.RunAgent
	}
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if agentMode {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
				"ok":    false,
				"error": map[string]any{"message": err.Error()},
			})
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wln: %v\n", err)
		os.Exit(1)
	}
}

func isAgentInvocation(path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return name == "wlna"
}

func offerUpdate(ctx context.Context, args []string) bool {
	if os.Getenv("WLN_NO_UPDATE_CHECK") != "" || !selfupdate.IsReleaseVersion(app.Version) || skipUpdateCheck(args) {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	info, err := selfupdate.Check(checkCtx, app.Version, false)
	cancel()
	if err != nil || !info.Available {
		return false
	}
	fmt.Fprintf(os.Stderr, "A new wln version is available: %s -> %s\n", app.Version, info.LatestVersion)
	if info.URL != "" {
		fmt.Fprintln(os.Stderr, info.URL)
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) || !isTerminal(os.Stderr) {
		fmt.Fprintln(os.Stderr, "Run 'wln update' to install it.")
		return false
	}
	fmt.Fprint(os.Stderr, "Update now before running the command? [Y/n] ")
	answer, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if readErr != nil && len(answer) == 0 {
		fmt.Fprintln(os.Stderr)
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "" && answer != "y" && answer != "yes" && answer != "д" && answer != "да" {
		return false
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, 5*time.Minute)
	result, err := selfupdate.Update(updateCtx, app.Version)
	updateCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wln: automatic update failed: %v\nContinuing with the current version.\n", err)
		return false
	}
	if result.Deferred {
		fmt.Fprintf(os.Stderr, "Downloaded wln %s. Windows will finish the update after this process exits.\nRun the command again.\n", result.Version)
	} else {
		fmt.Fprintf(os.Stderr, "Updated to wln %s. Run the command again.\n", result.Version)
	}
	return true
}

func skipUpdateCheck(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "help", "update", "-h", "--help", "--version":
			return true
		}
	}
	return false
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
