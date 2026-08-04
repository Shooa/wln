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
