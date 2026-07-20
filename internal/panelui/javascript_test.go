package panelui

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPanelUIJavaScript(t *testing.T) {
	nodeExecutable, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping Panel UI JavaScript tests")
	}
	versionOutput, err := exec.Command(nodeExecutable, "--version").Output()
	if err != nil {
		t.Skipf("could not determine node version: %v", err)
	}
	majorVersionText := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(versionOutput)), "v"), ".")[0]
	majorVersion, err := strconv.Atoi(majorVersionText)
	if err != nil || majorVersion < 18 {
		t.Skipf("node 18 or newer is required for Panel UI JavaScript tests; found %q", strings.TrimSpace(string(versionOutput)))
	}
	_, currentTestFile, _, callerAvailable := runtime.Caller(0)
	if !callerAvailable {
		t.Fatal("could not locate Panel UI JavaScript test directory")
	}

	testDirectory := filepath.Join(filepath.Dir(currentTestFile), "js_test")
	testFiles, err := filepath.Glob(filepath.Join(testDirectory, "*.test.mjs"))
	if err != nil {
		t.Fatalf("find Panel UI JavaScript tests: %v", err)
	}
	if len(testFiles) == 0 {
		t.Fatal("no Panel UI JavaScript tests found")
	}

	for _, testFile := range testFiles {
		command := exec.Command(nodeExecutable, testFile)
		command.Dir = filepath.Dir(currentTestFile)
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("Panel UI JavaScript test %s failed: %v\n%s", filepath.Base(testFile), commandErr, output)
		}
	}
}
