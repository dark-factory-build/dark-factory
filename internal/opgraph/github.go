package opgraph

import (
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// CIUnit is the service.name of a repository's GitHub Actions unit, unique
// per repository so the github adapter binds to the right one.
func CIUnit(repository string) string { return "github-actions:" + repository }

// CIKeys are the selector keys the github adapter reports for a CI unit.
var CIKeys = []string{"service.name", "cicd.pipeline.task.name"}

// CheckName is the name a job's check runs share: GitHub appends matrix
// values as " (…)", which a declared name spells as an expression.
func CheckName(name string) string {
	name, _, _ = strings.Cut(name, " (")
	name, _, _ = strings.Cut(name, "${{")
	return strings.TrimSpace(name)
}

func workflowFile(name string) bool {
	extension := path.Ext(name)
	return path.Dir(name) == ".github/workflows" && (extension == ".yml" || extension == ".yaml")
}

// workflow reads a GitHub Actions workflow: each of its jobs is a job of
// the repository's CI unit. Only jobs a pull request triggers bind to check
// runs, since the broker reads checks of pull request heads alone; the rest
// stay unbound, so CI silence never claims them idle.
func (run *inference) workflow(repo string, body []byte, at Location) {
	var file struct {
		Name string `yaml:"name"`
		On   any    `yaml:"on"`
		Jobs map[string]struct {
			Name string `yaml:"name"`
		} `yaml:"jobs"`
	}
	if yaml.Unmarshal(body, &file) != nil || len(file.Jobs) == 0 {
		return
	}
	var events []string
	switch value := file.On.(type) {
	case string:
		events = []string{value}
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				events = append(events, text)
			}
		}
	case map[string]any:
		events = keys(value)
	}
	pullRequest := contains(events, "pull_request") || contains(events, "pull_request_target")
	declared := static("github-actions", path.Base(at.Path), Declared)
	ci := run.addUnit(unit{repo: repo, root: path.Dir(at.Path), key: CIUnit(repo), label: "GitHub Actions", runtime: "ci",
		names: []string{CIUnit(repo)}, evidence: declared, at: Location{Repository: repo, Path: path.Dir(at.Path)}})
	workflow := file.Name
	if workflow == "" {
		workflow = at.Path // GitHub's own name for an unnamed workflow
	}
	for _, id := range keys(file.Jobs) {
		name := CheckName(file.Jobs[id].Name)
		if name == "" {
			name = id
		}
		var selectors map[string]string
		if pullRequest {
			selectors = map[string]string{"cicd.pipeline.task.name": name}
		}
		run.find(finding{owner: owner{units: []string{ci.key}}, kind: Job, key: "workflow:" + at.Path + "#" + id, label: workflow + " / " + name,
			edge: Runs, selectors: selectors, evidence: declared, at: at})
	}
}
