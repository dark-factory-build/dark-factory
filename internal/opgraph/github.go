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
func (run *inference) workflow(repo, name string, body []byte, at Location) {
	var file struct {
		Name string `yaml:"name"`
		On   any    `yaml:"on"`
		Jobs map[string]struct {
			Name     string `yaml:"name"`
			Defaults struct {
				Run struct {
					Dir string `yaml:"working-directory"`
				} `yaml:"run"`
			} `yaml:"defaults"`
			Steps []struct {
				Run string `yaml:"run"`
				Dir string `yaml:"working-directory"`
			} `yaml:"steps"`
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
	ci := run.addUnit(unit{repo: repo, root: path.Dir(at.Path), key: CIUnit(repo), label: "GitHub Actions · " + path.Base(name), runtime: "ci",
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
		job := file.Jobs[id]
		text := job.Defaults.Run.Dir
		for _, step := range job.Steps {
			text += " " + step.Dir + " " + step.Run
		}
		run.jobs = append(run.jobs, jobText{repo, ci.key, "workflow:" + at.Path + "#" + id, text, declared})
	}
}

// jobText is what a CI job touches: its working directories and commands.
type jobText struct {
	repo, ci, key, text string
	evidence            Evidence
}

// linkJobs joins a job to each unit whose source root its text names, as in
// a working-directory or a `go run ./tool`.
func (run *inference) linkJobs(units []*unit, builder *Builder) {
	for _, job := range run.jobs {
		var from string
		for _, ci := range units {
			if ci.repo == job.repo && ci.key == job.ci {
				from = ID(builder.system, ci.id, job.key)
			}
		}
		for _, field := range strings.Fields(job.text) {
			field = strings.Trim(strings.TrimPrefix(field, "./"), `"';`)
			for _, target := range units {
				if target.repo == job.repo && target.root != "." && target.key != job.ci && (field == target.root || strings.HasPrefix(field, target.root+"/")) {
					builder.Edge(from, target.id, Calls, job.evidence)
				}
			}
		}
	}
}
