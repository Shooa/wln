package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHierarchicalHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "root", args: []string{"help"}, want: []string{"COMMANDS", "wln help --all"}},
		{name: "bare root", args: nil, want: []string{"COMMANDS", "wln help --all"}},
		{name: "root flag", args: []string{"--help"}, want: []string{"COMMANDS", "GLOBAL OPTIONS"}},
		{name: "group", args: []string{"help", "units"}, want: []string{"SUBCOMMANDS", "status  Show connectivity"}},
		{name: "bare profile group", args: []string{"profile"}, want: []string{"wln profile", "login NAME", "check [NAME]"}},
		{name: "bare units group", args: []string{"units"}, want: []string{"wln units", "SUBCOMMANDS"}},
		{name: "bare messages group", args: []string{"messages"}, want: []string{"wln messages", "tail"}},
		{name: "bare api group", args: []string{"api"}, want: []string{"wln api", "call SERVICE"}},
		{name: "bare messages get", args: []string{"messages", "get"}, want: []string{"wln messages get UNIT", "--last DURATION", "--today", "--yesterday", "--since HH:MM"}},
		{name: "bare messages tail", args: []string{"messages", "tail"}, want: []string{"wln messages tail UNIT", "--follow", "--max-params"}},
		{name: "bare messages export", args: []string{"messages", "export"}, want: []string{"wln messages export UNIT", "--last", "--compress"}},
		{name: "bare profile login", args: []string{"profile", "login"}, want: []string{"wln profile login NAME", "--server BASE_URL", "--callback-timeout"}},
		{name: "bare api call", args: []string{"api", "call"}, want: []string{"wln api call SERVICE", "--params"}},
		{name: "leaf", args: []string{"help", "units", "status"}, want: []string{"--inactive", "selecting an unused unit"}},
		{name: "inline leaf", args: []string{"messages", "get", "--help"}, want: []string{"--last DURATION", "default is local midnight"}},
		{name: "inline after argument", args: []string{"messages", "get", "1001", "--help"}, want: []string{"wln messages get UNIT", "--yesterday"}},
		{name: "all", args: []string{"help", "--all"}, want: []string{"wln profile login", "wln messages tail", "wln api call", "wln update"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), tt.args, &stdout, &stderr); err != nil {
				t.Fatalf("Run(%q) error = %v", tt.args, err)
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout does not contain %q:\n%s", want, stdout.String())
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestArgumentErrorsPrintCanonicalHelp(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	tests := []struct {
		name string
		args []string
		err  string
		want []string
	}{
		{name: "get missing unit", args: []string{"--config", configPath, "messages", "get", "--last", "2h"}, err: "UNIT is required", want: []string{"INTERVAL", "--last DURATION", "--since HH:MM"}},
		{name: "tail missing unit", args: []string{"--config", configPath, "messages", "tail", "--follow"}, err: "UNIT is required", want: []string{"wln messages tail UNIT", "--poll DURATION"}},
		{name: "login missing name", args: []string{"--config", configPath, "profile", "login", "--server", "https://example.test"}, err: "profile NAME is required", want: []string{"wln profile login NAME", "--server BASE_URL"}},
		{name: "login missing server", args: []string{"--config", configPath, "profile", "login", "test"}, err: "--server is required", want: []string{"REQUIRED", "--server BASE_URL"}},
		{name: "api missing service", args: []string{"--config", configPath, "api", "call", "--params", "{}"}, err: "SERVICE is required", want: []string{"wln api call SERVICE", "--params"}},
		{name: "unknown subcommand", args: []string{"--config", configPath, "messages", "missing"}, err: "unknown messages subcommand", want: []string{"SUBCOMMANDS", "get", "tail", "export"}},
		{name: "unknown flag", args: []string{"--config", configPath, "messages", "get", "unit", "--missing"}, err: "flag provided but not defined", want: []string{"INTERVAL", "--last DURATION", "--yesterday"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), tt.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("error = %v, want containing %q", err, tt.err)
			}
			for _, want := range tt.want {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr does not contain %q:\n%s", want, stderr.String())
				}
			}
		})
	}
}

func TestUnknownHelpTopic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"help", "missing"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown help topic") {
		t.Fatalf("error = %v", err)
	}
}
