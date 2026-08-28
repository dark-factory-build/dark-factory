//go:build darwin

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testResultProof() ResultProof {
	var value [resultProofBytes]byte
	for index := range value {
		value[index] = byte(index + 1)
	}
	proof, _ := NewResultProof(value)
	return proof
}

func publicAttemptResult(result AttemptResult) AttemptResult {
	result.proof = ResultProof{}
	return result
}

func testResultProofHex() string {
	value, _ := encodeResultProof(testResultProof())
	return value
}

func openAttemptResultTestDir(t *testing.T, inner bool) (string, *os.File) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, OuterActivationMarkerName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if inner {
		if err := os.WriteFile(filepath.Join(root, InnerActivationMarkerName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return root, dir
}

func testConvergedAttemptResult(t *testing.T, attemptID string) AttemptResult {
	t.Helper()
	result, err := innerConvergedResult(attemptID, testResultProof(), Identity{PID: 22, PGID: 22, Birth: Birth{Seconds: 3, Microseconds: 4}}, Exit{Code: 0})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttemptResultNoReplaceCanonicalRoundTrip(t *testing.T) {
	root, dir := openAttemptResultTestDir(t, false)
	result, err := innerUnregisteredConvergedResult("attempt-1", testResultProof())
	if err != nil {
		t.Fatal(err)
	}
	record, err := publishAttemptResult(dir, result)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, AttemptResultSpoolName))
	if err != nil || string(body) != `{"version":1,"attempt_id":"attempt-1","kind":"inner_unregistered_converged","proof":"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"}` {
		t.Fatalf("canonical result = %q, %v", body, err)
	}
	if _, err := publishAttemptResult(dir, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement publication = %v", err)
	}
	loaded, err := AuthenticateAttemptResult(dir, "attempt-1", nil)
	if err != nil || loaded.Result() != publicAttemptResult(result) || loaded.Notice() != record.Notice() || loaded.InnerActivated() {
		t.Fatalf("authenticated result = %+v, %v", loaded, err)
	}
	if loaded, err = AuthenticateAttemptResult(dir, "attempt-1", ptrNotice(record.Notice())); err != nil || loaded.Result() != publicAttemptResult(result) {
		t.Fatalf("notice-bound result = %+v, %v", loaded, err)
	}
}

func TestAttemptResultForgedProofOnlyCreatesFailClosedConflict(t *testing.T) {
	root, dir := openAttemptResultTestDir(t, false)
	correctProof := testResultProof()
	wrongProof := correctProof
	wrongProof.value[0] ^= 0xff
	forgedResult, err := innerUnregisteredConvergedResult("forged", wrongProof)
	if err != nil {
		t.Fatal(err)
	}
	forgedBody, err := canonicalAttemptResult(forgedResult)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, AttemptResultSpoolName), forgedBody, 0o600); err != nil {
		t.Fatal(err)
	}
	forged, err := AuthenticateAttemptResult(dir, "forged", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotDigest := forged.ProofDigest()
	wantDigest := sha256.Sum256(correctProof.value[:])
	if gotDigest == wantDigest {
		t.Fatal("forged proof matched the Store-bound digest")
	}
	correctResult, _ := innerUnregisteredConvergedResult("forged", correctProof)
	if _, err := publishAttemptResult(dir, correctResult); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged residue did not fail closed: %v", err)
	}
}

func ptrNotice(value AttemptResultNotice) *AttemptResultNotice { return &value }

func TestAttemptResultConvergedCodeAndSignalAreClosed(t *testing.T) {
	for _, exit := range []Exit{{Code: 0}, {Code: -1, Signal: int(unix.SIGTERM)}} {
		t.Run(exitName(exit), func(t *testing.T) {
			_, dir := openAttemptResultTestDir(t, true)
			result, err := innerConvergedResult("attempt-converged", testResultProof(), Identity{PID: 23, PGID: 23, Birth: Birth{Seconds: 4, Microseconds: 5}}, exit)
			if err != nil {
				t.Fatal(err)
			}
			record, err := publishAttemptResult(dir, result)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := AuthenticateAttemptResult(dir, "attempt-converged", ptrNotice(record.Notice()))
			if err != nil || loaded.Result() != publicAttemptResult(result) || !loaded.InnerActivated() {
				t.Fatalf("converged result = %+v, %v", loaded, err)
			}
		})
	}
}

func exitName(exit Exit) string {
	if exit.Signal != 0 {
		return "signal"
	}
	return "code"
}

func TestAttemptResultRejectsMalformedOversizeAndAmbiguousJSON(t *testing.T) {
	proof := testResultProofHex()
	values := map[string]string{
		"unknown field":   fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":%q,"message":"x"}`, proof),
		"duplicate field": fmt.Sprintf(`{"version":1,"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":%q}`, proof),
		"trailing bytes":  fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":%q}\n`, proof),
		"both exits":      fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_converged","proof":%q,"process":{"pid":22,"pgid":22,"birth":{"seconds":3,"microseconds":4}},"exit":{"code":0,"signal":9}}`, proof),
		"missing process": fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_converged","proof":%q,"exit":{"code":0}}`, proof),
		"missing proof":   `{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged"}`,
		"short proof":     `{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":"00"}`,
		"uppercase proof": fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":%q}`, strings.ToUpper(proof)),
		"zero proof":      fmt.Sprintf(`{"version":1,"attempt_id":"attempt","kind":"inner_unregistered_converged","proof":%q}`, strings.Repeat("0", 64)),
	}
	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			root, dir := openAttemptResultTestDir(t, false)
			if err := os.WriteFile(filepath.Join(root, AttemptResultSpoolName), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := AuthenticateAttemptResult(dir, "attempt", nil); !errors.Is(err, ErrIdentity) {
				t.Fatalf("malformed load = %v", err)
			}
		})
	}
	t.Run("oversize", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, false)
		if err := os.WriteFile(filepath.Join(root, AttemptResultSpoolName), []byte(strings.Repeat("x", maxAttemptResultBytes+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateAttemptResult(dir, "attempt", nil); !errors.Is(err, ErrIdentity) {
			t.Fatalf("oversize load = %v", err)
		}
	})
}

func TestAttemptResultPartialWriteIsRetainedAndNeverRepaired(t *testing.T) {
	root, dir := openAttemptResultTestDir(t, false)
	result, _ := innerUnregisteredConvergedResult("partial", testResultProof())
	_, err := publishAttemptResultWithWrite(dir, result, func(fd int, body []byte) error {
		if _, writeErr := unix.Write(fd, body[:len(body)/2]); writeErr != nil {
			return writeErr
		}
		return io.ErrShortWrite
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial publication = %v", err)
	}
	if stat, statErr := os.Stat(filepath.Join(root, AttemptResultSpoolName)); statErr != nil || stat.Size() <= 0 {
		t.Fatalf("partial residue = %+v, %v", stat, statErr)
	}
	if _, err := AuthenticateAttemptResult(dir, "partial", nil); !errors.Is(err, ErrIdentity) {
		t.Fatalf("partial authentication = %v", err)
	}
	if _, err := publishAttemptResult(dir, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial repair = %v", err)
	}
}

func TestAttemptResultRejectsSymlinkHardlinkAndReplacement(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, false)
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, AttemptResultSpoolName)); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateAttemptResult(dir, "attempt", nil); err == nil {
			t.Fatal("symlink authenticated")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, false)
		result, _ := innerUnregisteredConvergedResult("hardlink", testResultProof())
		if _, err := publishAttemptResult(dir, result); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(root, AttemptResultSpoolName), filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateAttemptResult(dir, "hardlink", nil); !errors.Is(err, ErrIdentity) {
			t.Fatalf("hardlink authentication = %v", err)
		}
	})
	t.Run("replacement", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, true)
		original, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "replace"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, AttemptResultSpoolName), filepath.Join(root, "moved")); err != nil {
			t.Fatal(err)
		}
		replacementResult, _ := innerConvergedResult("replace", testResultProof(), Identity{PID: 24, PGID: 24, Birth: Birth{Seconds: 4, Microseconds: 5}}, Exit{Code: 1})
		if _, err := publishAttemptResult(dir, replacementResult); err != nil {
			t.Fatal(err)
		}
		if err := RemoveAttemptResult(dir, original); !errors.Is(err, ErrIdentity) {
			t.Fatalf("replacement removal = %v", err)
		}
		if _, err := AuthenticateAttemptResult(dir, "replace", nil); err != nil {
			t.Fatalf("replacement was removed: %v", err)
		}
	})
}

func TestAttemptResultRemovalMatchesExactRecordAndProvesAbsence(t *testing.T) {
	root, dir := openAttemptResultTestDir(t, true)
	record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "remove"))
	if err != nil {
		t.Fatal(err)
	}
	forged := *record
	forged.notice.Digest = strings.Repeat("0", 64)
	if err := RemoveAttemptResult(dir, &forged); !errors.Is(err, ErrIdentity) {
		t.Fatalf("forged removal = %v", err)
	}
	if err := RemoveAttemptResult(dir, record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, AttemptResultSpoolName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result remains after removal: %v", err)
	}
	if err := FinishAttemptResultRemoval(dir); err != nil {
		t.Fatalf("absence replay = %v", err)
	}
}

func TestAttemptResultRejectsImpossibleMarkerCensus(t *testing.T) {
	for _, test := range []struct {
		name    string
		residue []string
	}{
		{name: "gate config", residue: []string{GateConfigScratchName}},
		{name: "gate stdin", residue: []string{GateStdinScratchName}},
		{name: "both gate scratches", residue: []string{GateConfigScratchName, GateStdinScratchName}},
		{name: "legacy terminal", residue: []string{TerminalSpoolName}},
		{name: "legacy terminal scratch", residue: []string{TerminalScratchName}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, dir := openAttemptResultTestDir(t, true)
			record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "census"))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range test.residue {
				if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AuthenticateAttemptResult(dir, "census", ptrNotice(record.Notice())); !errors.Is(err, ErrIdentity) {
				t.Fatalf("impossible census authenticated: %v", err)
			}
		})
	}
	t.Run("inner without outer", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, true)
		record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "inner-only"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, OuterActivationMarkerName)); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateAttemptResult(dir, "inner-only", ptrNotice(record.Notice())); !errors.Is(err, ErrIdentity) {
			t.Fatalf("inner-only census authenticated: %v", err)
		}
	})
	t.Run("unregistered with inner", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, false)
		result, _ := innerUnregisteredConvergedResult("unregistered-inner", testResultProof())
		record, err := publishAttemptResult(dir, result)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, InnerActivationMarkerName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateAttemptResult(dir, "unregistered-inner", ptrNotice(record.Notice())); !errors.Is(err, ErrIdentity) {
			t.Fatalf("unregistered inner census authenticated: %v", err)
		}
	})
}

// TestAttemptNameCannotInflateCanonicalResultPastItsBound proves name
// validation guarantees byte-for-byte JSON encoding, so a length-valid name
// can never turn into a publish-time canonical-bound failure.
func TestAttemptNameCannotInflateCanonicalResultPastItsBound(t *testing.T) {
	for name, id := range map[string]string{
		"control": strings.Repeat("\x01", 200),
		"quote":   strings.Repeat(`"`, 200),
		"escape":  strings.Repeat(`\`, 200),
		"utf8":    strings.Repeat("é", 100),
		"delete":  "attempt\x7f",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := innerUnregisteredConvergedResult(id, testResultProof()); !errors.Is(err, ErrIdentity) {
				t.Fatalf("escape-inflating name accepted: %v", err)
			}
		})
	}
	longest := strings.Repeat("a", 256)
	result, err := innerUnregisteredConvergedResult(longest, testResultProof())
	if err != nil {
		t.Fatal(err)
	}
	body, err := canonicalAttemptResult(result)
	if err != nil || len(body) > maxAttemptResultBytes {
		t.Fatalf("longest valid name body = %d bytes, %v", len(body), err)
	}
}

// TestResultProofNeverEscapesDiagnosticFormatting proves no fmt verb, on any
// proof-carrying value or enclosing struct, can reproduce the raw or hex proof
// bytes. Stringer alone is not enough: %d bypasses it, and fmt cannot invoke
// methods on unexported fields at all.
func TestResultProofNeverEscapesDiagnosticFormatting(t *testing.T) {
	proof := testResultProof()
	raw := string(proof.value[:])
	hexProof := testResultProofHex()
	_, dir := openAttemptResultTestDir(t, true)
	record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "opacity"))
	if err != nil {
		t.Fatal(err)
	}
	live := testConvergedAttemptResult(t, "opacity")
	cfg := attemptConfig{Version: 1, AttemptID: "opacity", MarkerName: InnerActivationMarkerName, ResultName: AttemptResultSpoolName, ResultProof: hexProof}
	spec := AttemptSpec{AttemptID: "opacity", MarkerName: InnerActivationMarkerName, ResultName: AttemptResultSpoolName, ResultProof: proof}
	values := map[string]any{
		"proof":          proof,
		"result":         live,
		"record pointer": record,
		"record value":   *record,
		"config":         cfg,
		"spec":           spec,
	}
	for name, value := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X"} {
			rendered := fmt.Sprintf(verb, value)
			decoded := rendered
			if bytes, err := hex.DecodeString(strings.TrimSpace(rendered)); err == nil {
				decoded += string(bytes)
			}
			for _, needle := range []string{raw, hexProof, strings.ToUpper(hexProof)} {
				if strings.Contains(decoded, needle) {
					t.Fatalf("%s leaked the result proof through %s: %q", name, verb, rendered)
				}
			}
		}
	}
}

// TestAttemptResultMarkerMatrixIsClosed pins the complete kind × outer ×
// inner marker matrix on the writer, reader and removal paths. Every valid
// result requires the outer marker; inner_unregistered_converged additionally
// requires the inner marker absent; inner_converged permits either census.
func TestAttemptResultMarkerMatrixIsClosed(t *testing.T) {
	kinds := map[AttemptResultKind]func(t *testing.T, attemptID string) AttemptResult{
		AttemptResultInnerUnregisteredConverged: func(t *testing.T, attemptID string) AttemptResult {
			t.Helper()
			result, err := innerUnregisteredConvergedResult(attemptID, testResultProof())
			if err != nil {
				t.Fatal(err)
			}
			return result
		},
		AttemptResultInnerConverged: testConvergedAttemptResult,
	}
	setMarker := func(t *testing.T, root, name string, present bool) {
		t.Helper()
		path := filepath.Join(root, name)
		if present {
			if err := os.WriteFile(path, nil, 0o600); err != nil && !errors.Is(err, os.ErrExist) {
				t.Fatal(err)
			}
			return
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	for kind, build := range kinds {
		for _, outer := range []bool{true, false} {
			for _, inner := range []bool{true, false} {
				accept := outer && !(kind == AttemptResultInnerUnregisteredConverged && inner)
				name := fmt.Sprintf("%s outer=%t inner=%t", kind, outer, inner)
				t.Run("publish "+name, func(t *testing.T) {
					root, dir := openAttemptResultTestDir(t, false)
					setMarker(t, root, OuterActivationMarkerName, outer)
					setMarker(t, root, InnerActivationMarkerName, inner)
					record, err := publishAttemptResult(dir, build(t, "matrix"))
					if accept {
						if err != nil || record.InnerActivated() != inner {
							t.Fatalf("legal census publication = %+v, %v", record, err)
						}
						return
					}
					if !errors.Is(err, ErrIdentity) {
						t.Fatalf("forbidden census publication = %v", err)
					}
					if _, statErr := os.Stat(filepath.Join(root, AttemptResultSpoolName)); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("forbidden census left result residue: %v", statErr)
					}
				})
				t.Run("authenticate "+name, func(t *testing.T) {
					root, dir := openAttemptResultTestDir(t, false)
					if kind == AttemptResultInnerConverged {
						setMarker(t, root, InnerActivationMarkerName, true)
					}
					record, err := publishAttemptResult(dir, build(t, "matrix"))
					if err != nil {
						t.Fatal(err)
					}
					setMarker(t, root, OuterActivationMarkerName, outer)
					setMarker(t, root, InnerActivationMarkerName, inner)
					loaded, err := AuthenticateAttemptResult(dir, "matrix", ptrNotice(record.Notice()))
					if accept {
						if err != nil || loaded.InnerActivated() != inner {
							t.Fatalf("legal census authentication = %+v, %v", loaded, err)
						}
						return
					}
					if !errors.Is(err, ErrIdentity) {
						t.Fatalf("forbidden census authentication = %v", err)
					}
				})
			}
		}
	}
	t.Run("removal rejects outer marker loss", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, true)
		record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "matrix"))
		if err != nil {
			t.Fatal(err)
		}
		setMarker(t, root, OuterActivationMarkerName, false)
		if err := RemoveAttemptResult(dir, record); !errors.Is(err, ErrIdentity) {
			t.Fatalf("outerless removal = %v", err)
		}
	})
	t.Run("removal rejects unregistered inner marker gain", func(t *testing.T) {
		root, dir := openAttemptResultTestDir(t, false)
		result, err := innerUnregisteredConvergedResult("matrix", testResultProof())
		if err != nil {
			t.Fatal(err)
		}
		record, err := publishAttemptResult(dir, result)
		if err != nil {
			t.Fatal(err)
		}
		setMarker(t, root, InnerActivationMarkerName, true)
		if err := RemoveAttemptResult(dir, record); !errors.Is(err, ErrIdentity) {
			t.Fatalf("unregistered inner-gain removal = %v", err)
		}
	})
}

func TestAttemptResultRemovalReturnsDescriptorCloseUncertainty(t *testing.T) {
	_, dir := openAttemptResultTestDir(t, true)
	record, err := publishAttemptResult(dir, testConvergedAttemptResult(t, "close-uncertain"))
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("injected removal close uncertainty")
	old := testCloseAttemptResultRemoval
	t.Cleanup(func() { testCloseAttemptResultRemoval = old })
	testCloseAttemptResultRemoval = func(file *os.File) error {
		return errors.Join(file.Close(), want)
	}
	if err := RemoveAttemptResult(dir, record); !errors.Is(err, want) {
		t.Fatalf("removal discarded close uncertainty: %v", err)
	}
}
