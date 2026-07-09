package infra

import "testing"

func TestDestroyCommand(t *testing.T) {
	cmd := DestroyCommand("rg-aks-burner-test")
	want := []string{"az", "group", "delete", "--name", "rg-aks-burner-test", "--yes"}
	if len(cmd) != len(want) {
		t.Fatalf("len = %d, want %d", len(cmd), len(want))
	}
	for i := range want {
		if cmd[i] != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmd[i], want[i])
		}
	}
}
