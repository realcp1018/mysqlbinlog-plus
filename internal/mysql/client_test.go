package mysql

import "testing"

func TestGenerateServerID(t *testing.T) {
	if got := GenerateServerID(); got == 0 {
		t.Fatal("GenerateServerID returned 0")
	}
}
