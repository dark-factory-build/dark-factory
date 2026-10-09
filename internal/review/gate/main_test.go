package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The review gate decides every merge, so its failure modes are the point: a
// gate that cannot say NO reports green forever.

const (
	head  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// Synthetic GitHub identities: app is trusted by DF_REVIEW_TRUSTED_PUBLISHERS,
	// human by its OWNER association, and outsider by neither.
	app      = "101"
	human    = "202"
	outsider = "303"
	blk      = "11111111-1111-4111-8111-111111111111"
	fix      = "22222222-2222-4222-8222-222222222222"
)

// rec is one line of the workflow's @tsv projection.
func rec(commit, state, author, body string) string {
	assoc := map[string]string{app: "CONTRIBUTOR", human: "OWNER"}[author]
	if assoc == "" {
		assoc = "NONE"
	}
	return commit + "\t" + state + "\t" + author + "\t" + body + "\t" + assoc + "\n"
}

func allow(c string) string { return "Dark-Factory-Review: allow " + c }
func block(c string) string { return "Dark-Factory-Review: block " + c }

func blockMarked(c string) string {
	return "Finding. " + block(c) + " <!-- dark-factory-operation:" + blk + ":old-digest -->"
}

func correcting(c, extra string) string {
	return "Corrected. " + allow(c) + extra + " Dark-Factory-Review-Correction: " + blk + " <!-- dark-factory-operation:" + fix + ":new-digest -->"
}

// gate runs the decision over reviews with app trusted by id and returns
// exit code, output, summary.
func gate(t *testing.T, headSHA, reviews string) (int, string, string) {
	t.Helper()
	return gateTrusting(t, app, headSHA, reviews)
}

func gateTrusting(t *testing.T, trusted, headSHA, reviews string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	path, summary := filepath.Join(dir, "reviews"), filepath.Join(dir, "summary")
	if err := os.WriteFile(path, []byte(reviews), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DF_REVIEW_HEAD_SHA": headSHA, "DF_REVIEW_REVIEWS": path, "GITHUB_STEP_SUMMARY": summary, "DF_REVIEW_TRUSTED_PUBLISHERS": trusted}
	var out strings.Builder
	code := run(nil, func(k string) string { return env[k] }, &out, &out)
	written, _ := os.ReadFile(summary)
	return code, out.String(), string(written)
}

func TestDecision(t *testing.T) {
	long := strings.Repeat("x", 4200)
	for _, c := range []struct {
		name, reviews string
		pass          bool
		want          []string // in stdout+stderr or the summary
	}{
		{"allow at head", rec(head, "COMMENTED", app, "Could not break it. "+allow(head)), true, []string{"**ALLOWED**"}},
		{"no reviews", "", false, []string{"**NO VERDICT**"}},
		{"allow at a stale head", rec(other, "COMMENTED", app, allow(other)), false, nil},
		{"commit_id is a different head", rec(other, "COMMENTED", app, allow(head)), false, nil},
		{"rendered head is a different commit", rec(head, "COMMENTED", app, allow(other)), false, nil},
		{"note is not an allow", rec(head, "COMMENTED", app, "Dark-Factory-Review: note "+head), false, nil},
		{"longer rendered sha", rec(head, "COMMENTED", human, allow(head+"0")), false, nil},
		{"block outranks an allow", rec(head, "COMMENTED", app, allow(head)) + rec(head, "COMMENTED", app, block(head)), false, []string{"**BLOCKED**"}},
		{"other publisher block outranks an allow", rec(head, "COMMENTED", app, allow(head)) + rec(head, "COMMENTED", human, block(head)), false, []string{"**BLOCKED**"}},
		{"block in the same body outranks an allow", rec(head, "COMMENTED", human, allow(head)+" "+block(head)), false, []string{"**BLOCKED**"}},
		{"exact operation correction clears a block", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", app, correcting(head, "")), true, []string{"**ALLOWED**"}},
		{"another publisher cannot correct", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", human, correcting(head, "")), false, []string{"**BLOCKED**"}},
		{"unbound allow cannot clear a block", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", app, allow(head)), false, []string{"**BLOCKED**"}},
		{"correction before the verdict line", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", app, "Dark-Factory-Review-Correction: "+blk+" x "+allow(head)+" <!-- dark-factory-operation:"+fix+":d -->"), false, []string{"**BLOCKED**"}},
		{"correction separated from the verdict line", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", app, correcting(head, " extra text")), false, []string{"**BLOCKED**"}},
		{"block at the previous head", rec(other, "COMMENTED", app, block(other)) + rec(head, "COMMENTED", app, allow(head)), true, []string{"**ALLOWED**"}},
		{"CHANGES_REQUESTED blocks without a line", rec(head, "CHANGES_REQUESTED", app, "no verdict line"), false, []string{"**BLOCKED**"}},
		{"CHANGES_REQUESTED outranks an allow", rec(head, "COMMENTED", app, allow(head)) + rec(head, "CHANGES_REQUESTED", app, "no verdict line"), false, []string{"**BLOCKED**"}},
		{"CHANGES_REQUESTED is not correctable", rec(head, "CHANGES_REQUESTED", human, blockMarked(head)) + rec(head, "COMMENTED", human, correcting(head, "")), false, []string{"**BLOCKED**"}},
		{"APPROVED can allow", rec(head, "APPROVED", app, allow(head)), true, nil},
		{"empty body does not mask a verdict", rec(head, "COMMENTED", app, "") + rec(head, "COMMENTED", app, allow(head)), true, nil},
		{"empty body is not a verdict", rec(head, "COMMENTED", app, ""), false, nil},
		{"DISMISSED cannot allow", rec(head, "DISMISSED", human, allow(head)), false, nil},
		{"PENDING cannot allow", rec(head, "PENDING", human, allow(head)), false, nil},
		{"DISMISSED cannot block", rec(head, "DISMISSED", human, block(head)) + rec(head, "COMMENTED", app, allow(head)), true, nil},
		{"PENDING cannot block", rec(head, "PENDING", human, block(head)) + rec(head, "COMMENTED", app, allow(head)), true, nil},
		{"unterminated final record is read", rec(head, "COMMENTED", app, allow(head)) + strings.TrimSuffix(rec(head, "COMMENTED", app, block(head)), "\n"), false, []string{"**BLOCKED**"}},
		{"blank lines are skipped", "\n" + rec(head, "COMMENTED", app, allow(head)) + "\n", true, nil},
		{"three fields", head + "\tCOMMENTED\t" + app + "\n", false, []string{"malformed review record"}},
		{"two fields", head + "\tCOMMENTED\n", false, []string{"malformed review record"}},
		{"six fields", rec(head, "COMMENTED", app, allow(head)+"\tleftover"), false, []string{"malformed review record"}},
		{"OWNER allow passes", rec(head, "COMMENTED", human, allow(head)), true, []string{"**ALLOWED**"}},
		{"outsider allow is feedback", rec(head, "COMMENTED", outsider, "Looks fine. "+allow(head)), false, []string{"**NO VERDICT**", "untrusted publisher", "Looks fine."}},
		{"outsider block does not veto", rec(head, "COMMENTED", app, allow(head)) + rec(head, "COMMENTED", outsider, block(head)), true, []string{"**ALLOWED**"}},
		{"outsider CHANGES_REQUESTED does not veto", rec(head, "COMMENTED", app, allow(head)) + rec(head, "CHANGES_REQUESTED", outsider, "no"), true, []string{"**ALLOWED**"}},
		{"outsider cannot correct", rec(head, "COMMENTED", app, blockMarked(head)) + rec(head, "COMMENTED", outsider, correcting(head, "")), false, []string{"**BLOCKED**"}},
		{"contributor not in the list is untrusted", strings.Replace(rec(head, "COMMENTED", outsider, allow(head)), "NONE", "CONTRIBUTOR", 1), false, []string{"**NO VERDICT**"}},
		{"control bytes are neutralised", rec(head, "COMMENTED", app, "carriage\x1b[2Ktrick  "+block(head)), false, []string{"carriage?[2Ktrick"}},
		{"findings are escaped", rec(head, "COMMENTED", app, "Finding: <script>alert(1)</script> & the launch path. "+block(head)), false, []string{"&lt;script&gt;", "&amp; the launch path"}},
		{"long body is bounded", rec(head, "COMMENTED", app, long+"TAIL "+block(head)), false, []string{"**BLOCKED**"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, summary := gate(t, head, c.reviews)
			if (code == 0) != c.pass {
				t.Fatalf("exit %d, want pass=%v\n%s", code, c.pass, out)
			}
			for _, w := range c.want {
				if !strings.Contains(out+summary, w) {
					t.Fatalf("missing %q\n%s\n%s", w, out, summary)
				}
			}
			if strings.Contains(summary, "<script>") || strings.Contains(summary, "TAIL") {
				t.Fatalf("unbounded or unescaped body reached the summary:\n%s", summary)
			}
		})
	}

	// Maintainer associations are trusted with no list; listed ids by id.
	for _, a := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		r := head + "\tCOMMENTED\t" + outsider + "\t" + allow(head) + "\t" + a + "\n"
		if code, out, _ := gateTrusting(t, "", head, r); code != 0 {
			t.Fatalf("%s refused: %s", a, out)
		}
	}
	for trusted, pass := range map[string]bool{"": false, " ": false, "999": false, "999, 101": true} {
		if code, out, _ := gateTrusting(t, trusted, head, rec(head, "COMMENTED", app, allow(head))); (code == 0) != pass {
			t.Fatalf("trusted %q: exit %d %s", trusted, code, out)
		}
	}
	for _, trusted := range []string{"101,", "app", "101;202", "0", "-101"} {
		if code, out, _ := gateTrusting(t, trusted, head, rec(head, "COMMENTED", human, allow(head))); code == 0 || !strings.Contains(out, "DF_REVIEW_TRUSTED_PUBLISHERS") {
			t.Fatalf("malformed trusted %q: exit %d %s", trusted, code, out)
		}
	}
	// ponytail: interim four-field projection trusts any publisher, as before.
	if code, out, _ := gateTrusting(t, "", head, head+"\tCOMMENTED\t"+outsider+"\t"+allow(head)+"\n"); code != 0 {
		t.Fatalf("legacy record refused: %s", out)
	}
	// The publisher identity must be numeric.
	for _, p := range []string{"", "0", " 101", "reviewer-login"} {
		if code, out, _ := gate(t, head, rec(head, "COMMENTED", p, allow(head))); code == 0 || !strings.Contains(out, "malformed review publisher") {
			t.Fatalf("publisher %q: exit %d %s", p, code, out)
		}
	}
	if code, out, _ := gate(t, "not-a-sha", rec("not-a-sha", "COMMENTED", app, allow("not-a-sha"))); code == 0 || !strings.Contains(out, "is not a commit sha") {
		t.Fatalf("bad head: exit %d %s", code, out)
	}
	env := map[string]string{"DF_REVIEW_HEAD_SHA": head, "DF_REVIEW_REVIEWS": filepath.Join(t.TempDir(), "absent")}
	var out strings.Builder
	if run(nil, func(k string) string { return env[k] }, &out, &out) == 0 || !strings.Contains(out.String(), "must name a readable file") {
		t.Fatalf("missing reviews file: %s", out.String())
	}
}

func TestPullNumber(t *testing.T) {
	pull := func(ref string) (int, string) {
		var out, errs strings.Builder
		code := run([]string{"--pull-number"}, func(string) string { return ref }, &out, &errs)
		return code, out.String()
	}
	for ref, want := range map[string]string{
		"refs/heads/gh-readonly-queue/main/pr-325-f64d7d6457938b771ac55390d010d185dbddef1f": "325",
		"refs/heads/gh-readonly-queue/release/v1/pr-7-" + head:                              "7",
		// The base branch contains /pr-: leftmost parsing would read a
		// different pull request's verdict.
		"refs/heads/gh-readonly-queue/release/pr-2-hotfix/pr-341-" + head: "341",
	} {
		if code, got := pull(ref); code != 0 || got != want+"\n" {
			t.Fatalf("%s -> %d %q, want %s", ref, code, got, want)
		}
	}
	// Only the merge-queue form names a pull request; this gate runs nowhere else.
	for _, ref := range []string{"refs/heads/main", "", "refs/pull/330/merge", "refs/pull/abc/merge",
		"refs/heads/gh-readonly-queue/main/pr--abc", "refs/heads/gh-readonly-queue/main/pr-0-abc",
		"refs/heads/gh-readonly-queue/main/pr-07-abc"} {
		if code, got := pull(ref); code == 0 {
			t.Fatalf("%q resolved to %q", ref, got)
		}
	}
	var out strings.Builder
	if code := run([]string{"--pull-number", "x"}, os.Getenv, &out, &out); code != 64 {
		t.Fatalf("usage exit %d", code)
	}
}

// The App must render the verdict lines this gate reads, and the workflow must
// run the default branch's copy of this gate in the merge queue.
func TestWireContract(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rs, workflow := read("control-plane/src/github_app.rs"), read(".github/workflows/ci.yml")
	for _, w := range []string{
		`const REVIEW_VERDICT_PREFIX: &str = "Dark-Factory-Review:";`,
		`const REVIEW_CORRECTION_PREFIX: &str = "Dark-Factory-Review-Correction:";`,
		`const OPERATION_MARKER_PREFIX: &str = "<!-- dark-factory-operation:";`,
		// Every verdict is a COMMENT review: the App authors the pull requests
		// it reviews and GitHub refuses a self-review that takes a side.
		`const REVIEW_EVENT: &str = "COMMENT";`,
		`=> "allow",`, `=> "note",`, `=> "block",`,
	} {
		if !strings.Contains(rs, w) {
			t.Errorf("github_app.rs lacks %s", w)
		}
	}
	for _, w := range []string{
		"./internal/review/gate",
		"refs/heads/${DEFAULT_BRANCH}",
		"if: github.event_name == 'merge_group'",
		"(.user.id | tostring)",
	} {
		if !strings.Contains(workflow, w) {
			t.Errorf("ci.yml lacks %s", w)
		}
	}
}
