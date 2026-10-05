package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GH runs one `gh` command. It is the only seam: the real one writes to GitHub.
type GH func(arguments ...string) (stdout []byte, stderr string, err error)

var (
	publishAssetName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	httpNotFound      = regexp.MustCompile(`(?i)HTTP\s+404([^0-9]|$)`)
	transientResponse = regexp.MustCompile(`(?i)HTTP\s+5[0-9][0-9]([^0-9]|$)|error connecting to|connection (reset|refused)|unexpected EOF|(^|[^A-Za-z])EOF([^A-Za-z]|$)|timed? out|timeout|TLS handshake|temporary failure|stream error|remote end hung up`)
	errTransient      = errors.New("retryable GitHub response")
	publishRetryDelay = 2 * time.Second
)

const publishAttempts = 4

type releaseState struct {
	Draft      bool `json:"isDraft"`
	Prerelease bool `json:"isPrerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (state *releaseState) has(name string) bool {
	for _, asset := range state.Assets {
		if asset.Name == name {
			return true
		}
	}
	return false
}

type publisher struct {
	gh                      GH
	tag, commit, repository string
	digests                 map[string]string // asset name -> sha256:<hex>
}

// PublishRelease creates or resumes one draft release, uploads exactly the
// given assets, and publishes it:
//
//	TAG EXPECTED_COMMIT OWNER/REPO ASSET...
//
// Every write is preceded by a fresh read, and a failed write is followed by
// one, so a write that committed before its failure response is never
// repeated. Existing assets must have the local SHA-256, no other asset may
// exist, and the tag must peel to EXPECTED_COMMIT before creation and before
// publication. Transient GitHub and transport failures are retried four times.
func PublishRelease(arguments []string, gh GH) error {
	if len(arguments) < 4 {
		return errors.New("usage: publish TAG EXPECTED_COMMIT OWNER/REPO ASSET...")
	}
	p := publisher{gh: gh, tag: arguments[0], commit: arguments[1], repository: arguments[2], digests: map[string]string{}}
	if !releaseTag.MatchString(p.tag) || !validSource(p.commit) || !repositoryName.MatchString(p.repository) {
		return errors.New("invalid release tag, expected commit, or repository")
	}
	paths := arguments[3:]
	for _, path := range paths {
		name := filepath.Base(path)
		if !publishAssetName.MatchString(name) {
			return fmt.Errorf("release asset has an unsupported name: %s", name)
		}
		if _, duplicate := p.digests[name]; duplicate {
			return fmt.Errorf("release assets have a duplicate name: %s", name)
		}
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		p.digests[name] = digest
	}
	release := []string{p.tag, "--repo", p.repository}

	if err := retry("tag verification", p.verifyTag); err != nil {
		return err
	}
	create := append([]string{"release", "create"}, release...)
	create = append(create, "--draft", "--verify-tag", "--title", p.tag, "--generate-notes")
	if strings.Contains(p.tag, "-") {
		create = append(create, "--prerelease")
	}
	if err := retry("release creation", func() error {
		return p.write(func(state *releaseState) (bool, error) { return state != nil, nil }, create...)
	}); err != nil {
		return err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if err := retry("upload of "+name, func() error {
			return p.write(func(state *releaseState) (bool, error) {
				if state == nil {
					return false, fmt.Errorf("release %s disappeared", p.tag)
				}
				return state.has(name), nil
			}, "release", "upload", p.tag, path, "--repo", p.repository)
		}); err != nil {
			return err
		}
	}
	if err := retry("tag verification", p.verifyTag); err != nil {
		return err
	}
	if err := retry("release publication", func() error {
		return p.write(func(state *releaseState) (bool, error) {
			if state == nil {
				return false, fmt.Errorf("release %s disappeared", p.tag)
			}
			for name := range p.digests {
				if !state.has(name) {
					return false, fmt.Errorf("refusing to publish %s without the exact asset %s", p.tag, name)
				}
			}
			return !state.Draft, nil
		}, append(append([]string{"release", "edit"}, release...), "--draft=false", "--verify-tag")...)
	}); err != nil {
		return err
	}
	fmt.Println("GitHub release is complete:", p.tag)
	return nil
}

// write reads the release, runs the write unless done says it already took
// effect, and after a failed write reads once more: the write may have
// committed before its failure response arrived.
func (p *publisher) write(done func(*releaseState) (bool, error), arguments ...string) error {
	state, err := p.view()
	if err != nil {
		return err
	}
	if complete, err := done(state); err != nil || complete {
		return err
	}
	stdout, stderr, err := p.gh(arguments...)
	if err == nil {
		fmt.Print(string(stdout))
		fmt.Println("gh", strings.Join(arguments, " "))
		return nil
	}
	failure := ghError(arguments, stderr, err)
	if state, viewErr := p.view(); viewErr == nil {
		if complete, _ := done(state); complete {
			return nil
		}
	}
	return failure
}

// view returns nil when the release does not exist, and refuses a release
// with the wrong prerelease state, an unexpected asset, or a digest collision.
func (p *publisher) view() (*releaseState, error) {
	arguments := []string{"release", "view", p.tag, "--repo", p.repository, "--json", "isDraft,isPrerelease,assets"}
	stdout, stderr, err := p.gh(arguments...)
	if err != nil {
		if httpNotFound.MatchString(stderr) || strings.TrimSpace(stderr) == "release not found" {
			return nil, nil
		}
		return nil, ghError(arguments, stderr, err)
	}
	state := new(releaseState)
	if err := json.Unmarshal(stdout, state); err != nil {
		return nil, fmt.Errorf("release %s returned invalid state: %w", p.tag, err)
	}
	if expected := strings.Contains(p.tag, "-"); state.Prerelease != expected {
		return nil, fmt.Errorf("release %s has prerelease=%t; expected %t", p.tag, state.Prerelease, expected)
	}
	for _, asset := range state.Assets {
		digest, expected := p.digests[asset.Name]
		if !expected {
			return nil, fmt.Errorf("release %s has unexpected asset: %s", p.tag, asset.Name)
		}
		if asset.Digest != digest {
			return nil, fmt.Errorf("release asset %s already exists with a different SHA-256 digest", asset.Name)
		}
	}
	return state, nil
}

// verifyTag peels the remote tag through annotated tag objects; it must end
// at the expected commit.
func (p *publisher) verifyTag() error {
	endpoint := "repos/" + p.repository + "/git/ref/tags/" + p.tag
	for depth := 0; depth <= 8; depth++ {
		stdout, stderr, err := p.gh("api", endpoint)
		if err != nil {
			return ghError([]string{"api", endpoint}, stderr, err)
		}
		var ref struct {
			Object struct{ Type, SHA string } `json:"object"`
		}
		if err := json.Unmarshal(stdout, &ref); err != nil || !validSource(ref.Object.SHA) {
			return fmt.Errorf("tag %s returned an invalid object SHA", p.tag)
		}
		switch ref.Object.Type {
		case "commit":
			if ref.Object.SHA != p.commit {
				return fmt.Errorf("tag %s points to %s, expected %s", p.tag, ref.Object.SHA, p.commit)
			}
			return nil
		case "tag":
			endpoint = "repos/" + p.repository + "/git/tags/" + ref.Object.SHA
		default:
			return fmt.Errorf("tag %s points to unsupported object type: %s", p.tag, ref.Object.Type)
		}
	}
	return fmt.Errorf("tag %s has too many levels of indirection", p.tag)
}

func ghError(arguments []string, stderr string, err error) error {
	failure := fmt.Errorf("gh %s: %v: %s", strings.Join(arguments[:2], " "), err, strings.TrimSpace(stderr))
	if transientResponse.MatchString(stderr) {
		return fmt.Errorf("%w: %w", errTransient, failure)
	}
	return failure
}

// retry repeats only transient failures, with 2s, 4s, 8s backoff.
func retry(label string, operation func() error) error {
	delay := publishRetryDelay
	for attempt := 1; ; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, errTransient) {
			return fmt.Errorf("%s failed (attempt %d/%d): %w", label, attempt, publishAttempts, err)
		}
		if attempt == publishAttempts {
			return fmt.Errorf("%s failed after %d attempts: %w", label, publishAttempts, err)
		}
		fmt.Fprintf(os.Stderr, "%s: %v (attempt %d/%d); retrying in %s\n", label, err, attempt, publishAttempts, delay)
		time.Sleep(delay)
		delay *= 2
	}
}

func fileDigest(path string) (string, error) {
	information, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !information.Mode().IsRegular() {
		return "", fmt.Errorf("release asset is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
