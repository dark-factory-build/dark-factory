package change

import (
	"crypto/sha256"
	"net/url"
	"strings"
)

// RepositorySourceIdentity binds a primary checkout's administration and
// origin configuration across launches. A publication name is not a GitHub grant.
type RepositorySourceIdentity struct {
	Root                  RepositoryIdentity
	Git                   RepositoryIdentity
	OriginDigest          [32]byte
	PublicationRepository string
}

func (value RepositorySourceIdentity) Valid() bool {
	return value.Root.valid() && value.Git.valid() && value.Root.Device() == value.Git.Device() && value.OriginDigest != [32]byte{}
}

func publicationRepository(remote string) string {
	var path string
	if strings.HasPrefix(remote, "git@github.com:") {
		path = strings.TrimPrefix(remote, "git@github.com:")
	} else {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() != "github.com" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh") {
			return ""
		}
		if u.User != nil {
			if _, password := u.User.Password(); password || u.User.Username() != "git" {
				return ""
			}
		}
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(path, ".git")
	owner, name, found := strings.Cut(path, "/")
	if !found || owner == "" || name == "" || len(owner) > 39 || len(name) > 100 {
		return ""
	}
	for _, c := range owner + name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return ""
		}
	}
	return strings.ToLower(path)
}

func remoteSourceDigest(origin, push string) ([32]byte, string, error) {
	publication := publicationRepository(origin)
	if push != "" && push != origin && (publication == "" || publicationRepository(push) != publication) {
		return [32]byte{}, "", &ValidationError{Reason: "origin fetch and publication targets differ"}
	}
	// Keep credentials and local origin paths out of durable state.
	return sha256.Sum256([]byte(origin + "\x00" + push)), publication, nil
}
