package runner

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func folderScreen(t *testing.T) []byte {
	screen, err := os.ReadFile("testdata/codex-0.160.1-folder-access.raw")
	if err != nil {
		t.Fatal(err)
	}
	if !isCodexFolderAccessDialog(screen) {
		t.Fatal("fixture no longer matches; these tests would prove nothing")
	}
	return screen
}

// feed delivers output as the serve loop does: the read, then a tick.
func feed(o *terminalOwner, data []byte, now time.Time) {
	o.lastOutput = now
	o.answerFolderAccess(data, now)
	o.folderTick(now)
}

func TestCodexFolderAccessDialogIsRecognisedAcrossReads(t *testing.T) {
	screen := folderScreen(t)
	o, now := &terminalOwner{}, time.Now()
	for start := 0; start < len(screen); start += 7 {
		feed(o, screen[start:min(start+7, len(screen))], now)
	}
	if o.folderSeen.IsZero() {
		t.Fatal("captured Codex 0.160.1 dialog not recognised")
	}
}

func TestFolderAccessCRWaitsForQuietAndRetriesWhileIgnored(t *testing.T) {
	o, now := &terminalOwner{}, time.Now()
	feed(o, folderScreen(t), now)
	o.folderTick(now.Add(folderQuiet / 2))
	if o.folderSent != 0 {
		t.Fatal("CR sent before Codex went quiet; Codex would drop it")
	}
	o.folderTick(now.Add(folderQuiet))
	if o.folderSent != 1 {
		t.Fatalf("no CR after quiet: sent=%d", o.folderSent)
	}
	// Ignored: no output follows, so it is repeated, at most three times.
	for i, at := 2, now.Add(folderQuiet); i <= 3; i++ {
		at = at.Add(folderRetry)
		o.folderTick(at)
		if o.folderSent != uint8(i) {
			t.Fatalf("retry %d not sent: sent=%d", i, o.folderSent)
		}
		if i == 3 {
			o.folderTick(at.Add(folderRetry))
		}
	}
	if !o.folderDone || o.folderSent != 3 {
		t.Fatalf("retries unbounded: sent=%d done=%v", o.folderSent, o.folderDone)
	}
}

func TestFolderAccessOutputAfterCRStopsRetries(t *testing.T) {
	o, now := &terminalOwner{}, time.Now()
	feed(o, folderScreen(t), now)
	o.folderTick(now.Add(folderQuiet))
	feed(o, []byte("session started"), now.Add(folderQuiet+time.Millisecond))
	o.folderTick(now.Add(folderQuiet + folderRetry))
	if !o.folderDone || o.folderSent != 1 {
		t.Fatalf("answered dialog retried: sent=%d done=%v", o.folderSent, o.folderDone)
	}
}

func TestOtherStartupDialogsAreNeverAnswered(t *testing.T) {
	for _, screen := range []string{
		"\x1b[2;3HTrust this folder?\x1b[4;3H› 1. Trust and continue\x1b[5;3H2. Quit",
		"\x1b[2;3HFolder access\x1b[4;3H› 1. Trust and continue\x1b[5;3H2. Quit",
		"Welcome to Codex › 1. Sign in with ChatGPT",
	} {
		o, now := &terminalOwner{}, time.Now()
		feed(o, []byte(screen), now)
		o.folderTick(now.Add(folderRetry))
		if !o.folderSeen.IsZero() || o.folderSent != 0 {
			t.Fatalf("answered %q", screen)
		}
	}
}

func TestFolderAccessDialogAfterResumedTranscriptIsRecognised(t *testing.T) {
	o, now := &terminalOwner{}, time.Now()
	// A resumed session replays far more than any byte budget, and may quote
	// the dialog's words without its footer; neither is answered.
	feed(o, bytes.Repeat([]byte("replayed transcript: Folder access 1. Open restricted 2. Quit\r\n"), 1<<10), now)
	if !o.folderSeen.IsZero() {
		t.Fatal("a quoted dialog without its footer was recognised")
	}
	feed(o, folderScreen(t), now.Add(time.Second))
	if o.folderSeen.IsZero() {
		t.Fatal("dialog after a long resumed transcript was not recognised")
	}
}

func TestFolderAccessDialogAfterStartupWindowIsNeverAnswered(t *testing.T) {
	o, now := &terminalOwner{}, time.Now()
	feed(o, []byte("banner"), now)
	feed(o, []byte("ordinary output"), now.Add(folderWindow+time.Second))
	if !o.folderDone || o.folderTail != nil {
		t.Fatal("scan still active after the startup window")
	}
	feed(o, folderScreen(t), now.Add(folderWindow+2*time.Second))
	o.folderTick(now.Add(folderWindow + folderRetry*2))
	if !o.folderSeen.IsZero() || o.folderSent != 0 {
		t.Fatal("dialog after the window was answered")
	}
}
