package main

import "testing"

func TestSkipUpdateCheck(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"messages", "get", "--help"}, {"update"}, {"--version"}} {
		if !skipUpdateCheck(args) {
			t.Errorf("skipUpdateCheck(%q) = false", args)
		}
	}
	if skipUpdateCheck([]string{"units", "status"}) {
		t.Fatal("normal command unexpectedly skips update check")
	}
}

func TestIsAgentInvocation(t *testing.T) {
	for _, path := range []string{"wlna", "/usr/local/bin/wlna", `C:\\Tools\\wlna.exe`} {
		if !isAgentInvocation(path) {
			t.Errorf("isAgentInvocation(%q) = false", path)
		}
	}
	if isAgentInvocation("wln") {
		t.Fatal("wln unexpectedly enables agent mode")
	}
}
