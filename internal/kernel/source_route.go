package kernel

// EffectiveTaskText is the single task-text projection used by admission,
// authentication, and provider launch.
func EffectiveTaskText(provider Provider, title, body string) string {
	if provider != ProviderShell && body == "" {
		return title
	}
	return body
}
