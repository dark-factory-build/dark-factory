package runner

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestCodexFolderAccessDialogIsAnsweredOnceAcrossReads(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	o, start0 := &terminalOwner{}, time.Now()
	for start := 0; start < len(screen); start += 7 {
		o.answerFolderAccess(screen[start:min(start+7, len(screen))], start0)
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
		o.answerFolderAccess([]byte(screen), time.Now())
		if o.folderDone {
			t.Fatalf("answered %q", screen)
		}
	}
}

func TestFolderAccessDialogAfterResumedTranscriptIsAnswered(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	o, now := &terminalOwner{}, time.Now()
	// A resumed session replays far more than any byte budget, and may quote
	// the dialog's words without its footer; neither is answered.
	o.answerFolderAccess(bytes.Repeat([]byte("replayed transcript: Folder access 1. Open restricted 2. Quit\r\n"), 1<<10), now)
	if o.folderDone {
		t.Fatal("a quoted dialog without its footer was answered")
	}
	o.answerFolderAccess(screen, now.Add(time.Second))
	if !o.folderDone {
		t.Fatal("dialog after a long resumed transcript was not recognised")
	}
}

func TestFolderAccessDialogAfterStartupWindowIsNeverAnswered(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	o, now := &terminalOwner{}, time.Now()
	o.answerFolderAccess([]byte("banner"), now)
	o.answerFolderAccess([]byte("ordinary output"), now.Add(folderWindow+time.Second))
	if !o.folderDone || o.folderTail != nil {
		t.Fatal("scan still active after the startup window")
	}
	o.answerFolderAccess(screen, now.Add(folderWindow+2*time.Second)) // ignored: no CR
	if !isCodexFolderAccessDialog(screen) {
		t.Fatal("fixture no longer matches; this test would prove nothing")
	}
}
