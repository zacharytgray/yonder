package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/agents"
)

// regression (review of a68f72a): briefing paths reach a peer's shell; each
// stays one quoted word, so none runs as code
func TestPathsScriptQuotesEveryPath(t *testing.T) {
	home := t.TempDir()
	pwn := filepath.Join(t.TempDir(), "pwn")
	if err := os.MkdirAll(filepath.Join(home, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := []string{"~/repos", "/tmp/a';touch${IFS}" + pwn + ";'", "/tmp/$(touch " + pwn + ")", "~/a\"b;c&d", "~/x\\'y`touch " + pwn + "`"}
	cmd := exec.Command("sh", "-c", pathsScript(paths))
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	_, reply, ok := strings.Cut(string(out), agents.ReplyStart+"\n")
	if !ok || strings.Join(strings.Fields(reply), "") != "ynnnn" {
		t.Fatalf("reply: %q", out)
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("a path ran as code")
	}
}
