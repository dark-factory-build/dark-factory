// Command gate verifies independent-review attestations at one exact pull
// request head. Only a trusted publisher records a verdict: a repository
// owner, member or collaborator (GitHub's author_association), or a numeric
// user id listed in DF_REVIEW_TRUSTED_PUBLISHERS. Anyone else's review is
// feedback without allow or veto. The publisher authenticates the record;
// the reviewer it names is a separate identity and this gate does not prove
// its independence. GitHub permissions, protected merge and required CI still
// apply. The merge queue runs the default branch's copy, after review
// publication.
//
//	gate --pull-number   prints the pull request named by GITHUB_REF
//	gate                 decides DF_REVIEW_HEAD_SHA from DF_REVIEW_REVIEWS
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/review"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

var (
	digits  = regexp.MustCompile(`^[1-9][0-9]*$`)
	blockOp = regexp.MustCompile(`.*dark-factory-operation:([0-9a-f-]+):`)
)

func run(args []string, env func(string) string, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "verify-adversarial-review: "+format+"\n", a...)
		return 1
	}
	if len(args) > 0 {
		if len(args) != 1 || args[0] != "--pull-number" {
			fmt.Fprintln(stderr, "usage: gate [--pull-number]")
			return 64
		}
		// The merge queue names the entry's pull request only in GITHUB_REF:
		// refs/heads/gh-readonly-queue/<base>/pr-<n>-<base sha>. Its head is a
		// synthetic merge commit no pull request owns. Any other ref, such as
		// refs/pull/<n>/merge, means the trigger was widened: fail closed.
		ref := env("GITHUB_REF")
		rest, queued := strings.CutPrefix(ref, "refs/heads/gh-readonly-queue/")
		i := strings.Index(rest, "/pr-")
		if !queued || i < 0 || !strings.Contains(rest[i+4:], "-") {
			if ref == "" {
				ref = "<unset>"
			}
			return fail("cannot name a pull request from ref: %s", ref)
		}
		// Rightmost: a base branch may itself contain /pr-.
		number, _, _ := strings.Cut(ref[strings.LastIndex(ref, "/pr-")+4:], "-")
		if !digits.MatchString(number) {
			return fail("ref does not carry a pull number: %s", ref)
		}
		fmt.Fprintln(stdout, number)
		return 0
	}

	head, path := env("DF_REVIEW_HEAD_SHA"), env("DF_REVIEW_REVIEWS")
	if !review.HeadRE.MatchString(head) {
		return fail("DF_REVIEW_HEAD_SHA is not a commit sha")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("DF_REVIEW_REVIEWS must name a readable file")
	}

	trusted := map[string]bool{}
	if list := strings.TrimSpace(env("DF_REVIEW_TRUSTED_PUBLISHERS")); list != "" {
		for _, id := range strings.Split(list, ",") {
			if id = strings.TrimSpace(id); !digits.MatchString(id) {
				return fail("DF_REVIEW_TRUSTED_PUBLISHERS must list numeric user ids")
			}
			trusted[id] = true
		}
	}

	correction := regexp.MustCompile(`.*Dark-Factory-Review: allow ` + head + ` Dark-Factory-Review-Correction: ([0-9a-f-]+) <!-- dark-factory-operation:`)
	var findings strings.Builder
	var blocks [][2]string
	corrections := map[[2]string]bool{}
	allowed, considered := 0, 0
	// Each line is commit_id, state, numeric publisher id, flattened body and
	// author_association, exactly the workflow's @tsv projection; anything
	// else fails closed.
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		field := strings.Split(line, "\t")
		if len(field) != 5 {
			return fail("malformed review record")
		}
		commit, state, author, body := field[0], field[1], field[2], field[3]
		trust := trusted[author]
		switch field[4] {
		case "OWNER", "MEMBER", "COLLABORATOR":
			trust = true
		}
		// GitHub supplies the authenticated publisher identity.
		if !digits.MatchString(author) {
			return fail("malformed review publisher")
		}
		if commit != head || (state != "COMMENTED" && state != "APPROVED" && state != "CHANGES_REQUESTED") {
			continue
		}
		considered++
		verdict := "none"
		for _, v := range []string{"block", "allow", "note"} {
			if strings.Contains(" "+body+" ", " Dark-Factory-Review: "+v+" "+head+" ") {
				verdict = v
				break
			}
		}
		// An untrusted publisher's review is listed as feedback, never counted.
		// GitHub's own blocking state blocks even without a verdict line, and
		// its blocks carry no correctable operation.
		if !trust {
			verdict = "untrusted publisher"
		} else if state == "CHANGES_REQUESTED" {
			blocks = append(blocks, [2]string{author, ""})
			verdict = "block"
		} else if verdict == "block" {
			op := ""
			if m := blockOp.FindStringSubmatch(body); m != nil {
				op = m[1]
			}
			blocks = append(blocks, [2]string{author, op})
		} else if verdict == "allow" {
			allowed++
			// Only the original publisher can correct its own blocked operation.
			if m := correction.FindStringSubmatch(body); m != nil {
				corrections[[2]string{author, m[1]}] = true
			}
		}
		fmt.Fprintf(&findings, "<details><summary><code>%s</code> — <code>%s</code></summary>\n\n<pre>%s</pre>\n\n</details>\n\n",
			bounded(verdict), bounded(state), bounded(body))
	}

	// A correction clears only its publisher's exact blocked operation; a
	// block without one (including CHANGES_REQUESTED) stays conservative.
	blocked := 0
	for _, b := range blocks {
		if !corrections[b] { // an operation-less block never matches
			blocked++
		}
	}

	summary := fmt.Sprintf("### Adversarial review\n\n- Head: <code>%s</code>\n- Reviews at this head: <code>%d</code>\n\n%s", head, considered, findings.String())
	code := 1
	switch {
	case blocked > 0:
		summary += "**BLOCKED** — a trusted publisher recorded a blocking defect at this head.\nPush the fix; the new head needs a fresh verdict.\n"
		fail("%d blocking verdict(s) at %s", blocked, head)
	case allowed == 0:
		summary += "**NO VERDICT** — no trusted publisher has recorded an ALLOW at this head.\n\nA reviewer that did not write the change reviews the diff and records:\nPublish a GitHub review bound to this commit with `Dark-Factory-Review: allow HEAD_SHA`.\n"
		fail("no ALLOW verdict at %s", head)
	default:
		summary += fmt.Sprintf("**ALLOWED** — %d verdict(s) at this head, none blocking.\n", allowed)
		code = 0
	}
	if name := env("GITHUB_STEP_SUMMARY"); name != "" {
		f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err == nil {
			_, err = f.WriteString(summary)
			err = errors.Join(err, f.Close())
		}
		if err != nil {
			return fail("step summary: %v", err)
		}
	}
	if code == 0 {
		fmt.Fprintf(stdout, "verify-adversarial-review: %d allowing verdict(s) at %s\n", allowed, head)
	}
	return code
}

// bounded makes agent-authored text inert for the run summary: control bytes
// become '?', the length is capped at 4000 bytes, then HTML is escaped.
func bounded(s string) string {
	b := []byte(s)
	if len(b) > 4000 {
		b = b[:4000]
	}
	for i, c := range b {
		if c < 0x20 || c == 0x7f {
			b[i] = '?'
		}
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(string(b))
}
