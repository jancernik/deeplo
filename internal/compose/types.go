package compose

type DeployOptions struct {
	ComposeFiles []string
	PersistFiles []string
}

type DeployResult struct {
	Services      []ServiceStatus
	ComposeOutput string // combined stdout+stderr from `docker compose up`
}

// One row from `docker compose ps --format json`.
type ServiceStatus struct {
	ID         string      `json:"ID"`
	Name       string      `json:"Name"`
	Command    string      `json:"Command"`
	Project    string      `json:"Project"`
	Service    string      `json:"Service"`
	State      string      `json:"State"`
	Status     string      `json:"Status"`
	Health     string      `json:"Health"`
	ExitCode   int         `json:"ExitCode"`
	Publishers []Publisher `json:"Publishers"`
}

type Publisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}
