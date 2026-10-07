package runner

import (
	"os"
	"testing"
)

func TestCodexFolderAccessDialogIsAnsweredOnceAcrossReads(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	o := &terminalOwner{}
	for start := 0; start < len(screen); start += 7 {
		o.answerFolderAccess(screen[start:min(start+7, len(screen))])
	}
	if !o.folderAnswered || o.folderTail != nil {
		t.Fatalf("captured Codex 0.160.1 dialog not recognised: answered=%v", o.folderAnswered)
	}
}

func TestOtherStartupDialogsAreNeverAnswered(t *testing.T) {
	for _, screen := range []string{
		"\x1b[2;3HTrust this folder?\x1b[4;3H› 1. Trust and continue\x1b[5;3H2. Quit",
		"\x1b[2;3HFolder access\x1b[4;3H› 1. Trust and continue\x1b[5;3H2. Quit",
		"Welcome to Codex › 1. Sign in with ChatGPT",
	} {
		o := &terminalOwner{}
		o.answerFolderAccess([]byte(screen))
		if o.folderAnswered {
			t.Fatalf("answered %q", screen)
		}
	}
}
