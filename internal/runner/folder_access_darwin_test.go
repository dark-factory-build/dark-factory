package runner

import (
	"bytes"
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
	if !o.folderDone || o.folderTail != nil {
		t.Fatalf("captured Codex 0.160.1 dialog not recognised: answered=%v", o.folderDone)
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
		if o.folderDone {
			t.Fatalf("answered %q", screen)
		}
	}
}

func TestFolderAccessTextAfterStartupIsNeverAnswered(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	o := &terminalOwner{}
	o.answerFolderAccess(bytes.Repeat([]byte("ordinary session output\r\n"), 1<<10))
	if o.folderTail != nil || !o.folderDone {
		t.Fatal("startup scan still active after the first 16 KiB")
	}
	o.answerFolderAccess(screen) // must be ignored: no CR is written
	if o.folderTail != nil {
		t.Fatal("later output re-armed the scan")
	}
	if !isCodexFolderAccessDialog(screen) {
		t.Fatal("fixture no longer matches; this test would prove nothing")
	}
}
