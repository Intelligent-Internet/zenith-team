package protocol

import "testing"

func TestTaskLabelIsThePrefixAndNumber(t *testing.T) {
	if got := TaskLabel("ENG", 14); got != "ENG-14" {
		t.Fatalf("TaskLabel(ENG, 14) = %q, want ENG-14", got)
	}
	if got := TaskLabel("SCA", 4); got != "SCA-4" {
		t.Fatalf("TaskLabel(SCA, 4) = %q, want SCA-4", got)
	}
}
