package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// makeValidConfig returns a minimal valid Config for use as a base in tests.
func makeValidConfig() *Config {
	return &Config{
		Hosts: []Host{
			{
				Name:      "vm-docker-1",
				Address:   "10.0.0.21",
				DeployDir: "/srv/deeplo/apps",
			},
		},
		Repos: []RepoConfig{
			{
				Name:   "infra",
				URL:    "git@github.com:myorg/infra.git",
				Branch: "main",
			},
		},
		Projects: []Project{
			{
				Name:         "paperless",
				Repo:         "infra",
				RepoSubdir:   "apps/paperless",
				ComposeFiles: []string{"compose.yml"},
				Targets:      []string{"vm-docker-1"},
				DeploySubdir: "paperless",
			},
		},
	}
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writeYAML: %v", err)
	}
	return path
}

// Load

func TestLoad(t *testing.T) {
	validYAML := `
hosts:
  - name: vm-docker-1
    address: 10.0.0.21
    deploy_dir: /srv/deeplo/apps
repos:
  - name: infra
    url: git@github.com:myorg/infra.git
    branch: main
projects:
  - name: paperless
    repo: infra
    repo_subdir: apps/paperless
    compose_files:
      - compose.yml
    targets:
      - vm-docker-1
`

	cases := []struct {
		name      string
		content   string
		wantErr   bool
		checkFunc func(t *testing.T, config *Config)
	}{
		{
			name:    "valid full config",
			content: validYAML,
			checkFunc: func(t *testing.T, config *Config) {
				if len(config.Hosts) != 1 || config.Hosts[0].Name != "vm-docker-1" {
					t.Errorf("hosts not parsed correctly")
				}
				if len(config.Repos) != 1 || config.Repos[0].Name != "infra" {
					t.Errorf("repos not parsed correctly")
				}
				if len(config.Projects) != 1 || config.Projects[0].Name != "paperless" {
					t.Errorf("projects not parsed correctly")
				}
				if config.Projects[0].Repo != "infra" {
					t.Errorf("project.repo: got %q, want infra", config.Projects[0].Repo)
				}
			},
		},
		{
			name: "persist_files parsed from YAML",
			content: `
hosts:
  - name: vm-docker-1
    address: 10.0.0.21
    deploy_dir: /srv/deeplo/apps
repos:
  - name: infra
    url: git@github.com:myorg/infra.git
    branch: main
projects:
  - name: paperless
    repo: infra
    repo_subdir: apps/paperless
    compose_files:
      - compose.yml
    targets:
      - vm-docker-1
    persist_files:
      - .env
      - secrets/api.key
`,
			checkFunc: func(t *testing.T, config *Config) {
				want := []string{".env", "secrets/api.key"}
				got := config.Projects[0].PersistFiles
				if len(got) != len(want) {
					t.Fatalf("persist_files: got %v, want %v", got, want)
				}
				for i, v := range want {
					if got[i] != v {
						t.Errorf("persist_files[%d]: got %q, want %q", i, got[i], v)
					}
				}
			},
		},
		{
			name: "extra_files parsed from YAML",
			content: `
hosts:
  - name: vm-docker-1
    address: 10.0.0.21
    deploy_dir: /srv/deeplo/apps
repos:
  - name: infra
    url: git@github.com:myorg/infra.git
    branch: main
projects:
  - name: paperless
    repo: infra
    repo_subdir: apps/paperless
    compose_files:
      - compose.yml
    targets:
      - vm-docker-1
    extra_files:
      - config/app.conf
      - certs/ca.pem
`,
			checkFunc: func(t *testing.T, config *Config) {
				want := []string{"config/app.conf", "certs/ca.pem"}
				got := config.Projects[0].ExtraFiles
				if len(got) != len(want) {
					t.Fatalf("extra_files: got %v, want %v", got, want)
				}
				for i, v := range want {
					if got[i] != v {
						t.Errorf("extra_files[%d]: got %q, want %q", i, got[i], v)
					}
				}
			},
		},
		{
			name:    "malformed YAML",
			content: "hosts: [not valid yaml: {",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			checkFunc: func(t *testing.T, config *Config) {
				if len(config.Hosts) != 0 {
					t.Errorf("expected zero-value config for empty file")
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeYAML(t, testCase.content)
			config, err := Load(path)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, testCase.wantErr)
			}
			if err == nil && testCase.checkFunc != nil {
				testCase.checkFunc(t, config)
			}
		})
	}

	t.Run("file not found", func(t *testing.T) {
		_, err := Load("/nonexistent/path/config.yml")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}

func TestHostEffectiveSSHSettings(t *testing.T) {
	host := Host{Name: "vm-1", Address: "10.0.0.1", User: "app", Port: 2222}
	if got := host.EffectiveUser("deploy"); got != "app" {
		t.Fatalf("EffectiveUser override = %q, want app", got)
	}
	if got := host.EffectivePort(22); got != 2222 {
		t.Fatalf("EffectivePort override = %d, want 2222", got)
	}

	defaulted := Host{Name: "vm-2", Address: "10.0.0.2"}
	if got := defaulted.EffectiveUser("deploy"); got != "deploy" {
		t.Fatalf("EffectiveUser default = %q, want deploy", got)
	}
	if got := defaulted.EffectivePort(22); got != 22 {
		t.Fatalf("EffectivePort default = %d, want 22", got)
	}
}

// Env var interpolation

func TestExpandString(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name:  "no references",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "simple variable",
			input: "${MY_ADDR}",
			env:   map[string]string{"MY_ADDR": "10.0.0.1"},
			want:  "10.0.0.1",
		},
		{
			name:  "variable with default used",
			input: "${MY_ADDR:-192.168.1.1}",
			env:   map[string]string{},
			want:  "192.168.1.1",
		},
		{
			name:  "variable with default not used when set",
			input: "${MY_ADDR:-192.168.1.1}",
			env:   map[string]string{"MY_ADDR": "10.0.0.5"},
			want:  "10.0.0.5",
		},
		{
			name:  "empty default is valid",
			input: "${MAYBE_EMPTY:-}",
			env:   map[string]string{},
			want:  "",
		},
		{
			name:    "missing required variable",
			input:   "${MISSING_VAR}",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name:  "variable set to empty string expands to empty",
			input: "[${SET_BUT_EMPTY}]",
			env:   map[string]string{"SET_BUT_EMPTY": ""},
			want:  "[]",
		},
		{
			name:  "multiple references in one value",
			input: "${SCHEME}://${HOST}/path",
			env:   map[string]string{"SCHEME": "https", "HOST": "example.com"},
			want:  "https://example.com/path",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for k, v := range testCase.env {
				t.Setenv(k, v)
			}

			out, err := expandString(testCase.input)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("expandString() error = %v, wantErr %v", err, testCase.wantErr)
			}
			if err == nil && out != testCase.want {
				t.Errorf("output: got %q, want %q", out, testCase.want)
			}
		})
	}
}

func TestLoadBytes_EnvInterpolation(t *testing.T) {
	t.Setenv("TEST_HOST_ADDR", "10.1.2.3")
	t.Setenv("TEST_REPO_URL", "git@github.com:org/repo.git")

	yaml := `
hosts:
  - name: vm-1
    address: ${TEST_HOST_ADDR}
    deploy_dir: /srv/deploys
repos:
  - name: myrepo
    url: ${TEST_REPO_URL}
projects:
  - name: app
    repo: myrepo
    repo_subdir: apps/app
    targets: [vm-1]
`
	config, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.Hosts[0].Address != "10.1.2.3" {
		t.Errorf("address: got %q, want 10.1.2.3", config.Hosts[0].Address)
	}
	if config.Repos[0].URL != "git@github.com:org/repo.git" {
		t.Errorf("url: got %q", config.Repos[0].URL)
	}
}

func TestLoadBytes_MissingEnvVar(t *testing.T) {
	_ = os.Unsetenv("DEFINITELY_NOT_SET_XYZ")
	yaml := `
hosts:
  - name: vm-1
    address: ${DEFINITELY_NOT_SET_XYZ}
    deploy_dir: /srv
repos:
  - name: r
    url: u
projects:
  - name: p
    repo: r
    repo_subdir: s
    targets: [vm-1]
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing env var, got nil")
	}
}

func TestLoadBytes_EnvInjectionPrevention(t *testing.T) {
	// An env var containing YAML special characters (newline, indentation, new keys).
	t.Setenv("DANGEROUS_VAR", "10.0.0.1\n    deploy_dir: /hacked")

	yaml := `
hosts:
  - name: vm-1
    address: ${DANGEROUS_VAR}
    deploy_dir: /srv
repos: [ { name: r, url: u } ]
projects: [ { name: p, repo: r, repo_subdir: s, targets: [vm-1] } ]
`
	config, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The address should be the literal string including the newline, NOT parsed as new YAML.
	want := "10.0.0.1\n    deploy_dir: /hacked"
	if config.Hosts[0].Address != want {
		t.Errorf("address: got %q, want %q", config.Hosts[0].Address, want)
	}
	// The original deploy_dir must remain untouched.
	if config.Hosts[0].DeployDir != "/srv" {
		t.Errorf("deploy_dir: got %q, want /srv", config.Hosts[0].DeployDir)
	}
}

// An unquoted reference fills the field's native type: ${PORT} an int, a
// duration a time.Duration.
func TestLoadBytes_EnvInTypedFields(t *testing.T) {
	t.Setenv("SSH_PORT", "2222")
	t.Setenv("POLL", "5m")

	yaml := `
hosts:
  - name: vm-1
    address: 10.0.0.1
    deploy_dir: /srv
    port: ${SSH_PORT}
repos:
  - name: r
    url: u
    trigger_mode: poll
    poll_interval: ${POLL}
projects:
  - name: p
    repo: r
    repo_subdir: s
    targets: [vm-1]
`
	config, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.Hosts[0].Port != 2222 {
		t.Errorf("port: got %d, want 2222", config.Hosts[0].Port)
	}
	if config.Repos[0].PollInterval != 5*time.Minute {
		t.Errorf("poll_interval: got %v, want 5m", config.Repos[0].PollInterval)
	}
}

// Quoting a reference forces a string result, keeping a numeric-looking value as text.
func TestLoadBytes_QuotedReferenceForcesString(t *testing.T) {
	t.Setenv("NUMERIC_HOST", "12345")

	yaml := `
hosts:
  - name: vm-1
    address: "${NUMERIC_HOST}"
    deploy_dir: /srv
repos:
  - name: r
    url: u
projects:
  - name: p
    repo: r
    repo_subdir: s
    targets: [vm-1]
`
	config, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.Hosts[0].Address != "12345" {
		t.Errorf("address: got %q, want \"12345\"", config.Hosts[0].Address)
	}
}

// Expansion reads the live environment each call, so a reload reflects an env
// change without a restart.
func TestLoadBytes_ReEvaluatesEnvAcrossCalls(t *testing.T) {
	yaml := `
hosts:
  - name: vm-1
    address: ${ROLLING_ADDR}
    deploy_dir: /srv
repos:
  - name: r
    url: u
projects:
  - name: p
    repo: r
    repo_subdir: s
    targets: [vm-1]
`
	t.Setenv("ROLLING_ADDR", "10.0.0.1")
	first, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if first.Hosts[0].Address != "10.0.0.1" {
		t.Fatalf("first address: got %q, want 10.0.0.1", first.Hosts[0].Address)
	}

	t.Setenv("ROLLING_ADDR", "10.0.0.2")
	second, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if second.Hosts[0].Address != "10.0.0.2" {
		t.Errorf("second address: got %q, want 10.0.0.2", second.Hosts[0].Address)
	}
}

// applyDefaults

func TestApplyDefaults(t *testing.T) {
	cases := []struct {
		name  string
		input Config
		check func(t *testing.T, config *Config)
	}{
		{
			name:  "repo trigger mode defaults to poll",
			input: Config{Repos: []RepoConfig{{Name: "r", URL: "u", Branch: "main"}}},
			check: func(t *testing.T, config *Config) {
				if config.Repos[0].TriggerMode != TriggerModePoll {
					t.Errorf("trigger_mode: got %q, want poll", config.Repos[0].TriggerMode)
				}
			},
		},
		{
			name:  "explicit repo trigger mode preserved",
			input: Config{Repos: []RepoConfig{{Name: "r", URL: "u", Branch: "main", TriggerMode: TriggerModeHybrid}}},
			check: func(t *testing.T, config *Config) {
				if config.Repos[0].TriggerMode != TriggerModeHybrid {
					t.Errorf("trigger_mode: got %q, want hybrid", config.Repos[0].TriggerMode)
				}
			},
		},
		{
			name:  "empty repo branch becomes main",
			input: Config{Repos: []RepoConfig{{Name: "r", URL: "u"}}},
			check: func(t *testing.T, config *Config) {
				if config.Repos[0].Branch != "main" {
					t.Errorf("branch: got %q, want main", config.Repos[0].Branch)
				}
			},
		},
		{
			name:  "explicit repo branch preserved",
			input: Config{Repos: []RepoConfig{{Name: "r", URL: "u", Branch: "develop"}}},
			check: func(t *testing.T, config *Config) {
				if config.Repos[0].Branch != "develop" {
					t.Errorf("branch: got %q, want develop", config.Repos[0].Branch)
				}
			},
		},
		{
			name:  "empty project compose_files gets default",
			input: Config{Projects: []Project{{Name: "p", RepoSubdir: "apps/p"}}},
			check: func(t *testing.T, config *Config) {
				if len(config.Projects[0].ComposeFiles) != 1 || config.Projects[0].ComposeFiles[0] != "compose.yml" {
					t.Errorf("compose_files: got %v, want [compose.yml]", config.Projects[0].ComposeFiles)
				}
			},
		},
		{
			name:  "explicit project compose_files preserved",
			input: Config{Projects: []Project{{Name: "p", ComposeFiles: []string{"docker-compose.yml"}}}},
			check: func(t *testing.T, config *Config) {
				if len(config.Projects[0].ComposeFiles) != 1 || config.Projects[0].ComposeFiles[0] != "docker-compose.yml" {
					t.Errorf("compose_files: got %v, want [docker-compose.yml]", config.Projects[0].ComposeFiles)
				}
			},
		},
		{
			name:  "empty watch_paths with repo_subdir gets default glob",
			input: Config{Projects: []Project{{Name: "p", RepoSubdir: "apps/myapp"}}},
			check: func(t *testing.T, config *Config) {
				want := "apps/myapp/**"
				if len(config.Projects[0].WatchPaths) != 1 || config.Projects[0].WatchPaths[0] != want {
					t.Errorf("watch_paths: got %v, want [%s]", config.Projects[0].WatchPaths, want)
				}
			},
		},
		{
			name:  "empty watch_paths without repo_subdir stays nil",
			input: Config{Projects: []Project{{Name: "p"}}},
			check: func(t *testing.T, config *Config) {
				if config.Projects[0].WatchPaths != nil {
					t.Errorf("watch_paths: got %v, want nil", config.Projects[0].WatchPaths)
				}
			},
		},
		{
			name:  "explicit watch_paths preserved",
			input: Config{Projects: []Project{{Name: "p", RepoSubdir: "apps/p", WatchPaths: []string{"custom/**"}}}},
			check: func(t *testing.T, config *Config) {
				if len(config.Projects[0].WatchPaths) != 1 || config.Projects[0].WatchPaths[0] != "custom/**" {
					t.Errorf("watch_paths: got %v, want [custom/**]", config.Projects[0].WatchPaths)
				}
			},
		},
		{
			name:  "empty deploy_subdir defaults to project name",
			input: Config{Projects: []Project{{Name: "myapp"}}},
			check: func(t *testing.T, config *Config) {
				if config.Projects[0].DeploySubdir != "myapp" {
					t.Errorf("deploy_subdir: got %q, want myapp", config.Projects[0].DeploySubdir)
				}
			},
		},
		{
			name:  "explicit deploy_subdir preserved",
			input: Config{Projects: []Project{{Name: "myapp", DeploySubdir: "custom-dir"}}},
			check: func(t *testing.T, config *Config) {
				if config.Projects[0].DeploySubdir != "custom-dir" {
					t.Errorf("deploy_subdir: got %q, want custom-dir", config.Projects[0].DeploySubdir)
				}
			},
		},
		{
			name:  "nil extra_files stays nil",
			input: Config{Projects: []Project{{Name: "p"}}},
			check: func(t *testing.T, config *Config) {
				if config.Projects[0].ExtraFiles != nil {
					t.Errorf("extra_files: got %v, want nil", config.Projects[0].ExtraFiles)
				}
			},
		},
		{
			name:  "explicit extra_files preserved",
			input: Config{Projects: []Project{{Name: "p", ExtraFiles: []string{"config/app.conf", "certs/ca.pem"}}}},
			check: func(t *testing.T, config *Config) {
				want := []string{"config/app.conf", "certs/ca.pem"}
				if len(config.Projects[0].ExtraFiles) != len(want) {
					t.Fatalf("extra_files: got %v, want %v", config.Projects[0].ExtraFiles, want)
				}
				for i, v := range want {
					if config.Projects[0].ExtraFiles[i] != v {
						t.Errorf("extra_files[%d]: got %q, want %q", i, config.Projects[0].ExtraFiles[i], v)
					}
				}
			},
		},
		{
			name:  "unset persist_files defaults to .env",
			input: Config{Projects: []Project{{Name: "p"}}},
			check: func(t *testing.T, config *Config) {
				want := []string{".env"}
				if !slices.Equal(config.Projects[0].PersistFiles, want) {
					t.Errorf("persist_files: got %v, want %v", config.Projects[0].PersistFiles, want)
				}
			},
		},
		{
			name:  "explicit empty persist_files opts out (stays empty)",
			input: Config{Projects: []Project{{Name: "p", PersistFiles: []string{}}}},
			check: func(t *testing.T, config *Config) {
				if len(config.Projects[0].PersistFiles) != 0 {
					t.Errorf("persist_files: got %v, want [] (opt-out preserved)", config.Projects[0].PersistFiles)
				}
			},
		},
		{
			name:  "explicit persist_files preserved (no .env injected)",
			input: Config{Projects: []Project{{Name: "p", PersistFiles: []string{"config/secrets.json"}}}},
			check: func(t *testing.T, config *Config) {
				want := []string{"config/secrets.json"}
				if !slices.Equal(config.Projects[0].PersistFiles, want) {
					t.Errorf("persist_files: got %v, want %v", config.Projects[0].PersistFiles, want)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := testCase.input
			config.ApplyDefaults()
			testCase.check(t, &config)
		})
	}
}

// Validate

func hasFieldContaining(issues []ValidationIssue, field string) bool {
	for _, issue := range issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func TestBlockingIssues(t *testing.T) {
	// Fully valid config: no blockers.
	if blocking := makeValidConfig().BlockingIssues(); len(blocking) != 0 {
		t.Errorf("valid config should have no blockers, got: %v", blocking)
	}

	// Hosts + repos but zero projects: the only issue is a warning, so no blockers.
	noProjects := makeValidConfig()
	noProjects.Projects = nil
	if blocking := noProjects.BlockingIssues(); len(blocking) != 0 {
		t.Errorf("hosts+repos with no projects should have no blockers, got: %v", blocking)
	}

	// Empty config: still blocked on missing hosts and repos.
	if blocking := (&Config{}).BlockingIssues(); len(blocking) == 0 {
		t.Error("empty config should still be blocked (missing hosts/repos)")
	}

	// Missing hosts: blocked (this is a structural gap, not the relaxed case).
	noHosts := makeValidConfig()
	noHosts.Hosts = nil
	if blocking := noHosts.BlockingIssues(); len(blocking) == 0 {
		t.Error("config missing hosts should be blocked")
	}

	// A malformed project (bad repo reference) is a real structural error: blocked.
	badProject := makeValidConfig()
	badProject.Projects[0].Repo = "does-not-exist"
	if blocking := badProject.BlockingIssues(); len(blocking) == 0 {
		t.Error("config with a bad project repo reference should be blocked")
	}
}

// A hosts+repos config with no projects yields exactly one issue, a warning.
func TestValidate_NoProjectsIsWarningNotError(t *testing.T) {
	deployConfig := makeValidConfig()
	deployConfig.Projects = nil

	issues := deployConfig.Validate()
	if len(issues) != 1 {
		t.Fatalf("expected exactly 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].Field != "projects" || issues[0].Severity != SeverityWarning {
		t.Errorf("expected a projects warning, got %+v", issues[0])
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name             string
		mutate           func(config *Config)
		wantErrors       int
		wantFieldPresent string // optional: must appear in at least one error
	}{
		{
			name:       "valid config",
			mutate:     nil,
			wantErrors: 0,
		},
		{
			name: "no hosts",
			mutate: func(config *Config) {
				config.Hosts = nil
			},
			// no hosts → error on hosts; project target cross-ref also fails
			wantErrors: 2,
		},
		{
			name: "host missing address",
			mutate: func(config *Config) {
				config.Hosts[0].Address = ""
			},
			wantErrors: 1,
		},
		{
			name: "host missing deploy_dir",
			mutate: func(config *Config) {
				config.Hosts[0].DeployDir = ""
			},
			wantErrors: 1,
		},
		{
			name: "duplicate host names",
			mutate: func(config *Config) {
				config.Hosts = append(config.Hosts, Host{
					Name:      "vm-docker-1",
					Address:   "10.0.0.99",
					DeployDir: "/srv/deeplo/apps",
				})
			},
			wantErrors: 1,
		},
		{
			name: "no repos",
			mutate: func(config *Config) {
				config.Repos = nil
			},
			// no repos → error on repos; project.repo cross-ref also fails
			wantErrors: 2,
		},
		{
			name: "repo missing url",
			mutate: func(config *Config) {
				config.Repos[0].URL = ""
			},
			wantErrors:       1,
			wantFieldPresent: "repos[0].url",
		},
		{
			name: "repo missing branch",
			mutate: func(config *Config) {
				config.Repos[0].Branch = ""
			},
			wantErrors:       1,
			wantFieldPresent: "repos[0].branch",
		},
		{
			name: "duplicate repo names",
			mutate: func(config *Config) {
				dup := config.Repos[0]
				config.Projects = append(config.Projects, Project{
					Name:         "other",
					Repo:         "infra",
					RepoSubdir:   "apps/other",
					ComposeFiles: []string{"compose.yaml"},
					Targets:      []string{"vm-docker-1"},
				})
				config.Repos = append(config.Repos, dup)
			},
			wantErrors: 1,
		},
		{
			name: "repo unknown trigger mode",
			mutate: func(config *Config) {
				config.Repos[0].TriggerMode = "cron"
			},
			wantErrors:       1,
			wantFieldPresent: "repos[0].trigger_mode",
		},
		{
			name: "no projects",
			mutate: func(config *Config) {
				config.Projects = nil
			},
			wantErrors: 1,
		},
		{
			name: "project missing repo",
			mutate: func(config *Config) {
				config.Projects[0].Repo = ""
			},
			wantErrors: 1,
		},
		{
			name: "project references unknown repo",
			mutate: func(config *Config) {
				config.Projects[0].Repo = "nonexistent-repo"
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].repo",
		},
		{
			name: "project missing repo_subdir",
			mutate: func(config *Config) {
				config.Projects[0].RepoSubdir = ""
			},
			wantErrors: 1,
		},
		{
			name: "project missing compose_files",
			mutate: func(config *Config) {
				config.Projects[0].ComposeFiles = nil
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].compose_files",
		},
		{
			name: "project missing targets",
			mutate: func(config *Config) {
				config.Projects[0].Targets = nil
			},
			wantErrors: 1,
		},
		{
			name: "project target references unknown host",
			mutate: func(config *Config) {
				config.Projects[0].Targets = []string{"nonexistent-host"}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].targets[0]",
		},
		{
			name: "duplicate project names",
			mutate: func(config *Config) {
				dup := config.Projects[0]
				config.Projects = append(config.Projects, dup)
			},
			wantErrors: 1,
		},
		{
			name: "project invalid watch path pattern",
			mutate: func(config *Config) {
				config.Projects[0].WatchPaths = []string{"apps/[broken"}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].watch_paths[0]",
		},
		{
			name: "project empty watch path pattern",
			mutate: func(config *Config) {
				config.Projects[0].WatchPaths = []string{""}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].watch_paths[0]",
		},
		{
			name: "project valid watch path patterns",
			mutate: func(config *Config) {
				config.Projects[0].WatchPaths = []string{"apps/**", "**/*.yaml", "config/[ab].yml"}
			},
			wantErrors: 0,
		},
		{
			name: "project valid persist_files",
			mutate: func(config *Config) {
				config.Projects[0].PersistFiles = []string{".env", "secrets/api.key"}
			},
			wantErrors: 0,
		},
		{
			name: "project persist_files empty name",
			mutate: func(config *Config) {
				config.Projects[0].PersistFiles = []string{""}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].persist_files[0]",
		},
		{
			name: "project persist_files absolute path",
			mutate: func(config *Config) {
				config.Projects[0].PersistFiles = []string{"/etc/secrets/.env"}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].persist_files[0]",
		},
		{
			name: "project persist_files path traversal",
			mutate: func(config *Config) {
				config.Projects[0].PersistFiles = []string{"../../etc/secrets/.env"}
			},
			wantErrors:       1,
			wantFieldPresent: "projects[0].persist_files[0]",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := makeValidConfig()
			if testCase.mutate != nil {
				testCase.mutate(config)
			}
			errs := config.Validate()
			if len(errs) != testCase.wantErrors {
				t.Errorf("errors: got %d, want %d: %v", len(errs), testCase.wantErrors, errs)
			}
			if testCase.wantFieldPresent != "" && !hasFieldContaining(errs, testCase.wantFieldPresent) {
				t.Errorf("expected an error on field %q, got: %v", testCase.wantFieldPresent, errs)
			}
		})
	}
}

func TestIndexes(t *testing.T) {
	deployConfig := makeValidConfig()

	repos := deployConfig.RepoIndex()
	if len(repos) != 1 {
		t.Fatalf("RepoIndex: got %d entries, want 1", len(repos))
	}
	if repos["infra"].URL != "git@github.com:myorg/infra.git" {
		t.Errorf("RepoIndex: wrong repo for key %q: %+v", "infra", repos["infra"])
	}

	hosts := deployConfig.HostIndex()
	if len(hosts) != 1 {
		t.Fatalf("HostIndex: got %d entries, want 1", len(hosts))
	}
	if hosts["vm-docker-1"].Address != "10.0.0.21" {
		t.Errorf("HostIndex: wrong host for key %q: %+v", "vm-docker-1", hosts["vm-docker-1"])
	}

	projects := deployConfig.ProjectIndex()
	if len(projects) != 1 {
		t.Fatalf("ProjectIndex: got %d entries, want 1", len(projects))
	}
	if projects["paperless"].Repo != "infra" {
		t.Errorf("ProjectIndex: wrong project for key %q: %+v", "paperless", projects["paperless"])
	}
}
