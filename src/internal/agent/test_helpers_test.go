package agent

import "testing"

func startTestSession(t *testing.T, a *Agent) {
	t.Helper()
	if err := a.StartSession("test", t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
