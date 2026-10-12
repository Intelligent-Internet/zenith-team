package main

import "testing"

func TestOutputPresentationIsNotACommand(t *testing.T) {
	_, err := executeCLI(t, newRootCmd, "output", "present")
	if err == nil {
		t.Fatal("retired output command is still available")
	}
}
