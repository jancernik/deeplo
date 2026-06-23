package planner_test

import (
	"testing"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
)

// helpers

func makeConfig(hosts []config.Host, repos []config.RepoConfig, projects []config.Project) *config.Config {
	return &config.Config{
		Hosts:    hosts,
		Repos:    repos,
		Projects: projects,
	}
}

func host(name, addr string) config.Host {
	return config.Host{Name: name, Address: addr, DeployDir: "/srv/apps"}
}

func repo(name, branch string) config.RepoConfig {
	return config.RepoConfig{
		Name:   name,
		URL:    "git@github.com:owner/" + name + ".git",
		Branch: branch,
	}
}

func project(name, repoName string, watchPaths, targets []string) config.Project {
	return config.Project{
		Name:         name,
		Repo:         repoName,
		WatchPaths:   watchPaths,
		Targets:      targets,
		ComposeFiles: []string{"compose.yaml"},
	}
}

func projectWithSubdir(name, repoName, subdir string, watchPaths, targets []string) config.Project {
	proj := project(name, repoName, watchPaths, targets)
	proj.RepoSubdir = subdir
	return proj
}

func event(repoName string, files ...string) planner.RepoEvent {
	return planner.RepoEvent{
		RepoName:     repoName,
		Branch:       "main",
		CommitSha:    "abc123",
		ChangedFiles: files,
	}
}

// Plan

func TestPlan_RepoFilter(t *testing.T) {
	cases := []struct {
		name        string
		eventRepo   string
		wantTargets int
	}{
		{"matching repo", "myrepo", 1},
		{"non-matching repo", "otherrepo", 0},
		{"empty event repo", "", 0},
	}

	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{project("app", "myrepo", nil, []string{"prod"})},
	)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			targets := planner.Plan(deployConfig, event(testCase.eventRepo, "README.md"))
			if len(targets) != testCase.wantTargets {
				t.Errorf("got %d targets, want %d", len(targets), testCase.wantTargets)
			}
		})
	}
}

func TestPlan_NoWatchPathsNoSubdir_AlwaysDeploy(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{project("app", "myrepo", nil, []string{"prod"})},
	)
	// No watch_paths, no repo_subdir → deploy on every repo event.
	targets := planner.Plan(deployConfig, event("myrepo", "anything/changed.go"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Project.Name != "app" {
		t.Errorf("project: got %q, want app", targets[0].Project.Name)
	}
}

func TestPlan_RepoSubdirDefaultsWatchPath_FileInside(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{projectWithSubdir("app", "myrepo", "services/web", nil, []string{"prod"})},
	)
	targets := planner.Plan(deployConfig, event("myrepo", "services/web/main.go"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (file inside repo_subdir)", len(targets))
	}
}

func TestPlan_RepoSubdirDefaultsWatchPath_FileOutside(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{projectWithSubdir("app", "myrepo", "services/web", nil, []string{"prod"})},
	)
	targets := planner.Plan(deployConfig, event("myrepo", "config/deeplo.yml"))
	if len(targets) != 0 {
		t.Fatalf("got %d targets, want 0 (file outside repo_subdir)", len(targets))
	}
}

func TestPlan_RepoSubdirDefaultsWatchPath_NilChangedFiles(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{projectWithSubdir("app", "myrepo", "services/web", nil, []string{"prod"})},
	)
	// nil ChangedFiles means unknown diff → always deploy, even with a subdir default.
	targets := planner.Plan(deployConfig, planner.RepoEvent{RepoName: "myrepo", Branch: "main", CommitSha: "abc", ChangedFiles: nil})
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (nil diff = unknown)", len(targets))
	}
}

func TestPlan_ExplicitWatchPathsOverrideSubdir(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{
			// watch_paths points outside the subdir - explicit setting wins.
			projectWithSubdir("app", "myrepo", "services/web", []string{"shared/**"}, []string{"prod"}),
		},
	)
	// File inside subdir but not in watch_paths → no deploy.
	if targets := planner.Plan(deployConfig, event("myrepo", "services/web/main.go")); len(targets) != 0 {
		t.Errorf("got %d targets, want 0 (explicit watch_paths overrides subdir)", len(targets))
	}
	// File matching watch_paths → deploys.
	if targets := planner.Plan(deployConfig, event("myrepo", "shared/utils.go")); len(targets) != 1 {
		t.Errorf("got %d targets, want 1 (explicit watch_paths match)", len(targets))
	}
}

func TestPlan_RepoSubdirDefaultsWatchPath_MonorepoIsolation(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{
			projectWithSubdir("web", "myrepo", "services/web", nil, []string{"prod"}),
			projectWithSubdir("api", "myrepo", "services/api", nil, []string{"prod"}),
		},
	)
	// Only api files changed → only api deploys.
	targets := planner.Plan(deployConfig, event("myrepo", "services/api/handler.go"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Project.Name != "api" {
		t.Errorf("project: got %q, want api", targets[0].Project.Name)
	}
}

func TestPlan_WatchPaths(t *testing.T) {
	cases := []struct {
		name         string
		watchPaths   []string
		changedFiles []string
		wantSelected bool
	}{
		{
			name:         "file matches pattern",
			watchPaths:   []string{"services/web/**"},
			changedFiles: []string{"services/web/main.go"},
			wantSelected: true,
		},
		{
			name:         "file does not match pattern",
			watchPaths:   []string{"services/web/**"},
			changedFiles: []string{"docs/README.md"},
			wantSelected: false,
		},
		{
			name:         "one of many files matches",
			watchPaths:   []string{"services/api/**"},
			changedFiles: []string{"docs/README.md", "services/api/handler.go"},
			wantSelected: true,
		},
		{
			name:         "multiple patterns, first matches",
			watchPaths:   []string{"services/**", "config/**"},
			changedFiles: []string{"services/web/main.go"},
			wantSelected: true,
		},
		{
			name:         "multiple patterns, second matches",
			watchPaths:   []string{"services/**", "config/**"},
			changedFiles: []string{"config/app.yaml"},
			wantSelected: true,
		},
		{
			name:         "no patterns match",
			watchPaths:   []string{"services/**", "config/**"},
			changedFiles: []string{"docs/README.md", "test/e2e_test.go"},
			wantSelected: false,
		},
	}

	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		nil,
	)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deployConfig.Projects = []config.Project{
				project("app", "myrepo", testCase.watchPaths, []string{"prod"}),
			}
			targets := planner.Plan(deployConfig, planner.RepoEvent{
				RepoName:     "myrepo",
				Branch:       "main",
				CommitSha:    "abc",
				ChangedFiles: testCase.changedFiles,
			})
			got := len(targets) > 0
			if got != testCase.wantSelected {
				t.Errorf("selected=%v, want %v", got, testCase.wantSelected)
			}
		})
	}
}

func TestPlan_MultipleProjects(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{
			host("prod", "10.0.0.1"),
			host("staging", "10.0.0.2"),
		},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{
			project("web", "myrepo", []string{"services/web/**"}, []string{"prod"}),
			project("api", "myrepo", []string{"services/api/**"}, []string{"staging"}),
			// infra uses a different repo - should not be selected for myrepo events
		},
	)

	// Push touches web and api.
	targets := planner.Plan(deployConfig, planner.RepoEvent{
		RepoName:     "myrepo",
		Branch:       "main",
		CommitSha:    "abc",
		ChangedFiles: []string{"services/web/main.go", "services/api/handler.go"},
	})
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	names := map[string]bool{}
	for _, tgt := range targets {
		names[tgt.Project.Name] = true
	}
	for _, want := range []string{"web", "api"} {
		if !names[want] {
			t.Errorf("expected project %q in targets", want)
		}
	}
}

func TestPlan_MultipleTargetHosts(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{
			host("prod-eu", "10.1.0.1"),
			host("prod-us", "10.2.0.1"),
		},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{
			project("app", "myrepo", nil, []string{"prod-eu", "prod-us"}),
		},
	)

	targets := planner.Plan(deployConfig, event("myrepo", "compose.yaml"))
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2 (one per host)", len(targets))
	}
	hosts := map[string]bool{}
	for _, tgt := range targets {
		hosts[tgt.Host.Name] = true
	}
	for _, want := range []string{"prod-eu", "prod-us"} {
		if !hosts[want] {
			t.Errorf("expected host %q in targets", want)
		}
	}
}

func TestPlan_UnknownHostSkipped(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{
			// "staging" is not in config.Hosts
			project("app", "myrepo", nil, []string{"prod", "staging"}),
		},
	)

	targets := planner.Plan(deployConfig, event("myrepo", "compose.yaml"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (unknown host silently skipped)", len(targets))
	}
	if targets[0].Host.Name != "prod" {
		t.Errorf("host: got %q, want prod", targets[0].Host.Name)
	}
}

func TestPlan_TargetFieldsPreserved(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{{
			Name:      "prod",
			Address:   "192.168.1.1",
			DeployDir: "/srv/apps",
		}},
		[]config.RepoConfig{{
			Name:   "myrepo",
			URL:    "git@github.com:owner/myrepo.git",
			Branch: "main",
		}},
		[]config.Project{project("app", "myrepo", nil, []string{"prod"})},
	)

	targets := planner.Plan(deployConfig, event("myrepo"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	h := targets[0].Host
	if h.Address != "192.168.1.1" {
		t.Errorf("Address: got %q", h.Address)
	}
	if h.DeployDir != "/srv/apps" {
		t.Errorf("DeployDir: got %q", h.DeployDir)
	}
	r := targets[0].Repo
	if r.Name != "myrepo" {
		t.Errorf("Repo.Name: got %q", r.Name)
	}
	if r.URL != "git@github.com:owner/myrepo.git" {
		t.Errorf("Repo.URL: got %q", r.URL)
	}
}

func TestPlan_ForcedTargets_BypassesPlanLogic(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1"), host("staging", "10.0.0.2")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{project("app", "myrepo", nil, []string{"prod", "staging"})},
	)
	forced := []planner.DeployTarget{{
		Project: config.Project{Name: "app", Repo: "myrepo"},
		Host:    config.Host{Name: "staging", Address: "10.0.0.2"},
	}}
	targets := planner.Plan(deployConfig, planner.RepoEvent{
		RepoName:      "myrepo",
		Branch:        "main",
		CommitSha:     "abc123",
		ForcedTargets: forced,
	})
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1 (ForcedTargets bypasses plan)", len(targets))
	}
	if targets[0].Host.Name != "staging" {
		t.Errorf("host = %q, want staging", targets[0].Host.Name)
	}
}

func TestPlan_NoProjects(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		nil,
	)
	targets := planner.Plan(deployConfig, event("myrepo", "anything.go"))
	if targets != nil {
		t.Errorf("expected nil, got %v", targets)
	}
}

func TestPlan_NoHosts(t *testing.T) {
	deployConfig := makeConfig(
		nil,
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{project("app", "myrepo", nil, []string{"prod"})},
	)
	targets := planner.Plan(deployConfig, event("myrepo"))
	if len(targets) != 0 {
		t.Errorf("expected 0 targets with no hosts, got %d", len(targets))
	}
}

func TestPlan_ProjectDifferentRepo_NotSelected(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{
			repo("repo-a", "main"),
			repo("repo-b", "main"),
		},
		[]config.Project{
			project("app-a", "repo-a", nil, []string{"prod"}),
			project("app-b", "repo-b", nil, []string{"prod"}),
		},
	)
	// Event for repo-a should only select app-a
	targets := planner.Plan(deployConfig, event("repo-a", "compose.yaml"))
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	if targets[0].Project.Name != "app-a" {
		t.Errorf("project: got %q, want app-a", targets[0].Project.Name)
	}
}

// AllTargets

func TestAllTargets(t *testing.T) {
	cases := []struct {
		name         string
		deployConfig *config.Config
		wantTargets  int
	}{
		{
			name: "one target per host",
			deployConfig: makeConfig(
				[]config.Host{host("prod", "10.0.0.1"), host("staging", "10.0.0.2")},
				[]config.RepoConfig{repo("myrepo", "main")},
				[]config.Project{project("app", "myrepo", nil, []string{"prod", "staging"})},
			),
			wantTargets: 2,
		},
		{
			name: "multiple projects expanded",
			deployConfig: makeConfig(
				[]config.Host{host("prod", "10.0.0.1")},
				[]config.RepoConfig{repo("myrepo", "main")},
				[]config.Project{
					project("app", "myrepo", nil, []string{"prod"}),
					project("api", "myrepo", nil, []string{"prod"}),
				},
			),
			wantTargets: 2,
		},
		{
			name: "project with unknown repo is skipped",
			deployConfig: makeConfig(
				[]config.Host{host("prod", "10.0.0.1")},
				[]config.RepoConfig{repo("myrepo", "main")},
				[]config.Project{project("app", "ghost", nil, []string{"prod"})},
			),
			wantTargets: 0,
		},
		{
			name: "unknown host targets are skipped",
			deployConfig: makeConfig(
				[]config.Host{host("prod", "10.0.0.1")},
				[]config.RepoConfig{repo("myrepo", "main")},
				[]config.Project{project("app", "myrepo", nil, []string{"prod", "ghost"})},
			),
			wantTargets: 1,
		},
		{
			name:         "empty config",
			deployConfig: makeConfig(nil, nil, nil),
			wantTargets:  0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			targets := planner.AllTargets(testCase.deployConfig)
			if len(targets) != testCase.wantTargets {
				t.Errorf("got %d targets, want %d", len(targets), testCase.wantTargets)
			}
		})
	}
}

func TestAllTargets_PopulatesTargetFields(t *testing.T) {
	deployConfig := makeConfig(
		[]config.Host{host("prod", "10.0.0.1")},
		[]config.RepoConfig{repo("myrepo", "main")},
		[]config.Project{project("app", "myrepo", nil, []string{"prod"})},
	)
	targets := planner.AllTargets(deployConfig)
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(targets))
	}
	target := targets[0]
	if target.Project.Name != "app" {
		t.Errorf("project: got %q, want app", target.Project.Name)
	}
	if target.Host.Name != "prod" {
		t.Errorf("host: got %q, want prod", target.Host.Name)
	}
	if target.Repo.Name != "myrepo" {
		t.Errorf("repo: got %q, want myrepo", target.Repo.Name)
	}
}

// MatchPath

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Exact match
		{"compose.yaml", "compose.yaml", true},
		{"compose.yaml", "other.yaml", false},

		// Single-level glob
		{"*.yaml", "compose.yaml", true},
		{"*.yaml", "sub/compose.yaml", false},
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},

		// Double-star: trailing wildcard
		{"services/**", "services/web/main.go", true},
		{"services/**", "services/web/sub/main.go", true},
		{"services/**", "services/main.go", true},
		{"services/**", "other/main.go", false},

		// Double-star: zero segments
		{"services/**", "services", true}, // ** matches zero segments, so bare dir name matches
		{"a/**/b", "a/b", true},           // ** matches zero segments
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/c", false},

		// Double-star anywhere
		{"**/*.yaml", "compose.yaml", true},
		{"**/*.yaml", "sub/compose.yaml", true},
		{"**/*.yaml", "a/b/c.yaml", true},
		{"**/*.yaml", "a/b/c.go", false},

		// Leading double-star
		{"**/main.go", "main.go", true},
		{"**/main.go", "sub/main.go", true},
		{"**/main.go", "a/b/main.go", true},
		{"**/main.go", "a/b/other.go", false},

		// Multi-segment literal
		{"services/web/main.go", "services/web/main.go", true},
		{"services/web/main.go", "services/web/other.go", false},

		// Single-char wildcard
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.pattern+"_"+testCase.path, func(t *testing.T) {
			got := planner.MatchPath(testCase.pattern, testCase.path)
			if got != testCase.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v", testCase.pattern, testCase.path, got, testCase.want)
			}
		})
	}
}

// TriggerSource

// Pins the on-the-wire TriggerSource strings; they are persisted and
// user-visible, so changing them is a contract change.
func TestTriggerSourceValues(t *testing.T) {
	cases := []struct {
		source planner.TriggerSource
		want   string
	}{
		{planner.TriggerWebhook, "webhook"},
		{planner.TriggerPoll, "poll"},
		{planner.TriggerReconcileAddition, "addition"},
		{planner.TriggerReconcileProjectChange, "change"},
		{planner.TriggerResume, "resume"},
		{planner.TriggerRedeploy, "redeploy"},
	}
	for _, testCase := range cases {
		if string(testCase.source) != testCase.want {
			t.Errorf("TriggerSource = %q, want %q", string(testCase.source), testCase.want)
		}
	}
}
