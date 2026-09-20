package gitauthor

import "testing"

func TestIdentity(t *testing.T) {
	for _, identity := range []Identity{{}, {ID: 123, Login: "operator"}, {ID: 123, Login: "renamed-user"}} {
		if !identity.Valid() {
			t.Fatalf("valid identity rejected: %+v", identity)
		}
	}
	identity := Identity{ID: 123, Login: "operator"}
	if identity.Name() != "operator" || identity.Email() != "123+operator@users.noreply.github.com" {
		t.Fatal(identity)
	}
	if (Identity{}).Name() != AutomationName || (Identity{}).Email() != AutomationEmail {
		t.Fatal("fallback drift")
	}
	for _, identity := range []Identity{{ID: -1, Login: "operator"}, {ID: 1}, {Login: "operator"}, {ID: 1, Login: "bad@email"}, {ID: 1, Login: "bad\nname"}, {ID: 1, Login: "-name"}, {ID: 1, Login: "name-"}, {ID: 1, Login: "名前"}} {
		if identity.Valid() {
			t.Fatalf("invalid identity accepted: %+v", identity)
		}
	}
}
