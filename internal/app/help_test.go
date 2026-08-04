package app

import (
	"bytes"
	"context"
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
		{name: "root flag", args: []string{"--help"}, want: []string{"COMMANDS", "GLOBAL OPTIONS"}},
		{name: "group", args: []string{"help", "units"}, want: []string{"SUBCOMMANDS", "status  Show connectivity"}},
		{name: "leaf", args: []string{"help", "units", "status"}, want: []string{"--inactive", "selecting an unused unit"}},
		{name: "inline leaf", args: []string{"messages", "get", "--help"}, want: []string{"--last DURATION", "default is local midnight"}},
		{name: "inline after argument", args: []string{"messages", "get", "1001", "--help"}, want: []string{"wln messages get UNIT", "--yesterday"}},
		{name: "all", args: []string{"help", "--all"}, want: []string{"wln profile login", "wln messages tail", "wln api call"}},
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

func TestUnknownHelpTopic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"help", "missing"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown help topic") {
		t.Fatalf("error = %v", err)
	}
}
