package browserprotocol

import (
	"net/url"
	"strings"
)

// GitHubConnection is the paired operator's private GitHub settings bridge.
// The browser sees only bounded public metadata; credentials stay in the host.
type GitHubConnection struct {
	Action         string             `json:"action"`
	Code           string             `json:"code,omitempty"`
	Page           int                `json:"page,omitempty"`
	InstallationID int64              `json:"installation_id,omitempty"`
	Repositories   []GitHubDelegation `json:"repositories,omitempty"`
}

type GitHubDelegation struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository"`
}

type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type,omitempty"`
}

type GitHubAuthorization struct {
	ConnectionID string  `json:"connection_id"`
	URL          string  `json:"authorization_url"`
	ExpiresAt    Decimal `json:"expires_at"`
}

type GitHubStatus struct {
	ConnectionID string             `json:"connection_id"`
	State        string             `json:"state"`
	User         *GitHubUser        `json:"github_user,omitempty"`
	Repositories []GitHubDelegation `json:"repositories"`
}

type GitHubInstallation struct {
	ID          int64      `json:"id"`
	Account     GitHubUser `json:"account"`
	SuspendedAt *string    `json:"suspended_at"`
	URL         string     `json:"html_url,omitempty"`
	Eligibility string     `json:"eligibility"`
}

type GitHubInstallations struct {
	Items    []GitHubInstallation `json:"installations"`
	NextPage *int                 `json:"next_page"`
}

type GitHubRepository struct {
	ID          int64  `json:"id"`
	Name        string `json:"full_name"`
	Permissions struct {
		Pull     bool `json:"pull"`
		Push     bool `json:"push"`
		Maintain bool `json:"maintain"`
		Admin    bool `json:"admin"`
	} `json:"permissions"`
}

type GitHubRepositories struct {
	Items    []GitHubRepository `json:"repositories"`
	NextPage *int               `json:"next_page"`
}

type GitHubConnectionResult struct {
	State         string               `json:"state"`
	Authorization *GitHubAuthorization `json:"authorization,omitempty"`
	Status        *GitHubStatus        `json:"status,omitempty"`
	Installations *GitHubInstallations `json:"installations,omitempty"`
	Repositories  *GitHubRepositories  `json:"repositories,omitempty"`
}

func EncodeGitHubConnectionResult(id string, value GitHubConnectionResult) ([]byte, error) {
	return encodeControl(TypeGitHubConnectionResult, id, value)
}

func validGitHubConnection(kind MessageType, body any) error {
	bad := func() error { return ErrMalformed }
	switch value := body.(type) {
	case GitHubConnection:
		if value.Action != "connect" && value.Action != "confirm" && value.Action != "status" && value.Action != "refresh" && value.Action != "disconnect" && value.Action != "installations" && value.Action != "repositories" && value.Action != "delegate" {
			return bad()
		}
		if value.Action != "confirm" && value.Code != "" || value.Action != "delegate" && len(value.Repositories) != 0 || value.Page < 0 || value.Page > 1000 || value.InstallationID < 0 || value.Action != "installations" && value.Action != "repositories" && value.Page != 0 {
			return bad()
		}
		if value.Action == "confirm" && (len(value.Code) != 10 || strings.Trim(value.Code, "0123456789ABCDEF") != "") {
			return bad()
		}
		if value.Action == "installations" && value.Page < 1 || value.Action == "repositories" && (value.Page < 1 || value.InstallationID < 1) {
			return bad()
		}
		if value.Action != "repositories" && value.InstallationID != 0 || len(value.Repositories) > 100 {
			return bad()
		}
		for _, item := range value.Repositories {
			if item.InstallationID < 1 || item.RepositoryID < 1 || !validGitHubRepository(item.Repository) {
				return bad()
			}
		}
	case GitHubConnectionResult:
		if validateBoundedText(value.State, 1, 64) != nil {
			return bad()
		}
		if value.Authorization != nil && (validateBoundedText(value.Authorization.ConnectionID, 1, 128) != nil || validateBoundedText(value.Authorization.URL, 1, 2048) != nil || !validGitHubAuthorizationURL(value.Authorization.URL)) {
			return bad()
		}
		if value.Status != nil {
			if validateBoundedText(value.Status.ConnectionID, func() int {
				if value.Status.State == "disconnected" {
					return 0
				}
				return 1
			}(), 128) != nil || validateBoundedText(value.Status.State, 1, 64) != nil || len(value.Status.Repositories) > 100 {
				return bad()
			}
			if value.Status.User != nil && !validGitHubUser(*value.Status.User) {
				return bad()
			}
			for _, item := range value.Status.Repositories {
				if !validGitHubDelegation(item) {
					return bad()
				}
			}
		}
		if value.Installations != nil {
			if len(value.Installations.Items) > MaxJSONArray || !validGitHubPage(value.Installations.NextPage) {
				return bad()
			}
			for _, item := range value.Installations.Items {
				if item.ID < 1 || !validGitHubUser(item.Account) || item.SuspendedAt != nil && validateBoundedText(*item.SuspendedAt, 1, 128) != nil || item.URL != "" && validateBoundedText(item.URL, 1, 2048) != nil || validateBoundedText(item.Eligibility, 0, 64) != nil {
					return bad()
				}
			}
		}
		if value.Repositories != nil {
			if len(value.Repositories.Items) > MaxJSONArray || !validGitHubPage(value.Repositories.NextPage) {
				return bad()
			}
			for _, item := range value.Repositories.Items {
				if item.ID < 1 || !validGitHubRepository(item.Name) {
					return bad()
				}
			}
		}
	}
	return nil
}

func validGitHubPage(value *int) bool {
	return value == nil || *value >= 1 && *value <= 1000
}

func validGitHubAuthorizationURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() == "github.com" && parsed.User == nil && parsed.Path == "/login/oauth/authorize"
}

func validGitHubUser(value GitHubUser) bool {
	return value.ID > 0 && validateBoundedText(value.Login, 1, 128) == nil && (value.Type == "" || validateBoundedText(value.Type, 1, 32) == nil)
}

func validGitHubDelegation(value GitHubDelegation) bool {
	return value.InstallationID > 0 && value.RepositoryID > 0 && validGitHubRepository(value.Repository)
}

func validGitHubRepository(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 39 || len(parts[1]) < 1 || len(parts[1]) > 100 {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("/._-", character)) {
				return false
			}
		}
	}
	return true
}
