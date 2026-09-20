// Package gitauthor keeps local commit attribution consistent with the verified
// GitHub operator. It carries identity only, never GitHub authority or tokens.
package gitauthor

import "strconv"

const AutomationName = "Dark Factory"
const AutomationEmail = "worker@darkfactory.build"

// Identity's zero value is the factory identity used without a connected user.
type Identity struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (identity Identity) Valid() bool {
	if identity.ID == 0 && identity.Login == "" {
		return true
	}
	if identity.ID <= 0 || len(identity.Login) < 1 || len(identity.Login) > 39 || identity.Login[0] == '-' || identity.Login[len(identity.Login)-1] == '-' {
		return false
	}
	for _, c := range identity.Login {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (identity Identity) Name() string {
	if identity.ID == 0 {
		return AutomationName
	}
	return identity.Login
}
func (identity Identity) Email() string {
	if identity.ID == 0 {
		return AutomationEmail
	}
	return strconv.FormatInt(identity.ID, 10) + "+" + identity.Login + "@users.noreply.github.com"
}
