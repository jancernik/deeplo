package state

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type FileStore struct {
	dataPath string
	mu       sync.Mutex
}

func NewFileStore(dataPath string) *FileStore {
	return &FileStore{dataPath: dataPath}
}

func (store *FileStore) Init() error {
	for _, dir := range []string{
		filepath.Join(store.dataPath, "state", "poll"),
		filepath.Join(store.dataPath, "history", "runs"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create state directory %q: %w", dir, err)
		}
	}
	return nil
}

func (store *FileStore) SaveDeployment(deployment *Deployment) error {
	if deployment == nil {
		return errors.New("deployment is nil")
	}
	if strings.ContainsAny(deployment.ID, "/\\") {
		return fmt.Errorf("deployment ID %q is invalid", deployment.ID)
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	runFilePath := filepath.Join(store.dataPath, "history", "runs", deployment.ID+".json")
	if err := writeJSON(runFilePath, deployment); err != nil {
		return fmt.Errorf("write run file %q: %w", deployment.ID, err)
	}
	if err := store.updateProjectsIndex(deployment); err != nil {
		return fmt.Errorf("update latest deployment for host %q project %q: %w", deployment.Host, deployment.Project, err)
	}
	return nil
}

func (store *FileStore) updateProjectsIndex(deployment *Deployment) error {
	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	latestDeployments := make(map[string]*Deployment)

	if err := readJSON(projectsFilePath, &latestDeployments); err != nil && !os.IsNotExist(err) {
		return err
	}

	hostProjectKey := indexKeyForProjectHost(deployment.Project, deployment.Host)
	currentLatest, exists := latestDeployments[hostProjectKey]

	if !exists || currentLatest.ID == deployment.ID || !deployment.StartedAt.Before(currentLatest.StartedAt) {
		latestDeployments[hostProjectKey] = deployment
	}

	return writeJSON(projectsFilePath, latestDeployments)
}

func (store *FileStore) ListDeployments(project, host string, limit int) ([]*Deployment, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	runsDirectoryPath := filepath.Join(store.dataPath, "history", "runs")
	dirEntries, err := os.ReadDir(runsDirectoryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Deployment{}, nil
		}
		return nil, fmt.Errorf("read runs directory: %w", err)
	}

	deployments := []*Deployment{}
	var parseErrors []error
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".json") {
			continue
		}
		var deployment Deployment
		if err := readJSON(filepath.Join(runsDirectoryPath, dirEntry.Name()), &deployment); err != nil {
			parseErrors = append(parseErrors, fmt.Errorf("parse %q: %w", dirEntry.Name(), err))
			continue
		}
		if err := validateDeploymentRecord(&deployment); err != nil {
			parseErrors = append(parseErrors, fmt.Errorf("parse %q: %w", dirEntry.Name(), err))
			continue
		}
		if project != "" && deployment.Project != project {
			continue
		}
		if host != "" && deployment.Host != host {
			continue
		}
		deployments = append(deployments, &deployment)
	}

	sort.Slice(deployments, func(i, j int) bool {
		return deployments[i].StartedAt.After(deployments[j].StartedAt)
	})

	if limit > 0 && len(deployments) > limit {
		deployments = deployments[:limit]
	}
	return deployments, errors.Join(parseErrors...)
}

func (store *FileStore) GetLatestDeployment(project, host string) (*Deployment, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.readLatestDeploymentByKey(indexKeyForProjectHost(project, host))
}

func (store *FileStore) readLatestDeploymentByKey(hostProjectKey string) (*Deployment, error) {
	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	var deployments map[string]*Deployment
	if err := readJSON(projectsFilePath, &deployments); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	deployment := deployments[hostProjectKey]
	if deployment == nil {
		return nil, nil
	}
	if err := validateDeploymentRecord(deployment); err != nil {
		return nil, fmt.Errorf("read latest deployment for key %q: %w", hostProjectKey, err)
	}
	return deployment, nil
}

func (store *FileStore) ListLatestDeployments() ([]*Deployment, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	var latestDeployments map[string]*Deployment
	if err := readJSON(projectsFilePath, &latestDeployments); err != nil {
		if os.IsNotExist(err) {
			return []*Deployment{}, nil
		}
		return nil, err
	}

	deployments := make([]*Deployment, 0, len(latestDeployments))
	for _, deployment := range latestDeployments {
		if err := validateDeploymentRecord(deployment); err != nil {
			return nil, fmt.Errorf("read latest deployments: %w", err)
		}
		deployments = append(deployments, deployment)
	}
	sort.Slice(deployments, func(i, j int) bool {
		if deployments[i].Project != deployments[j].Project {
			return deployments[i].Project < deployments[j].Project
		}
		return deployments[i].Host < deployments[j].Host
	})
	return deployments, nil
}

func (store *FileStore) DeleteLatestDeployment(project, host string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	var latestDeployments map[string]*Deployment
	if err := readJSON(projectsFilePath, &latestDeployments); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	hostProjectKey := indexKeyForProjectHost(project, host)
	if _, exists := latestDeployments[hostProjectKey]; !exists {
		return false, nil
	}
	delete(latestDeployments, hostProjectKey)

	if err := writeJSON(projectsFilePath, latestDeployments); err != nil {
		return false, fmt.Errorf("write projects index: %w", err)
	}
	return true, nil
}

func (store *FileStore) MarkStaleRunningDeploymentsFailed() (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	var latestDeployments map[string]*Deployment
	if err := readJSON(projectsFilePath, &latestDeployments); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read projects index: %w", err)
	}

	var updated int
	now := time.Now().UTC()
	for key, deployment := range latestDeployments {
		if deployment.Status != StatusRunning {
			continue
		}
		deployment.Status = StatusFailed
		deployment.Error = "interrupted: daemon shutdown while deploy was in progress"
		deployment.CompletedAt = &now
		latestDeployments[key] = deployment

		runFilePath := filepath.Join(store.dataPath, "history", "runs", deployment.ID+".json")
		if err := writeJSON(runFilePath, deployment); err != nil {
			return updated, fmt.Errorf("update run file %q: %w", deployment.ID, err)
		}
		updated++
	}

	if updated == 0 {
		return 0, nil
	}
	if err := writeJSON(projectsFilePath, latestDeployments); err != nil {
		return updated, fmt.Errorf("write projects index: %w", err)
	}
	return updated, nil
}

func (store *FileStore) SaveRepoState(repoState *RepoState) error {
	if repoState == nil {
		return errors.New("repo state is nil")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	snapshot := *repoState
	snapshot.UpdatedAt = time.Now().UTC()
	path := store.repoStatePath(snapshot.Repo, snapshot.Branch)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create repo state dir: %w", err)
	}
	return writeJSON(path, &snapshot)
}

func (store *FileStore) GetRepoState(repoName, branch string) (*RepoState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var repoState RepoState
	if err := readJSON(store.repoStatePath(repoName, branch), &repoState); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &repoState, nil
}

func (store *FileStore) repoStatePath(repoName, branch string) string {
	return filepath.Join(store.dataPath, "state", "poll", sanitizePathSegment(repoName), sanitizePathSegment(branch)+".json")
}

func indexKeyForProjectHost(projectName, hostName string) string {
	return hostName + "/" + projectName
}

func validateDeploymentRecord(deployment *Deployment) error {
	if deployment == nil {
		return errors.New("deployment record is nil")
	}
	if deployment.ID == "" {
		return errors.New("deployment record has empty ID")
	}
	if deployment.Project == "" {
		return fmt.Errorf("deployment %q has empty project", deployment.ID)
	}
	if deployment.Host == "" {
		return fmt.Errorf("deployment %q has empty host", deployment.ID)
	}
	return nil
}

var pathSegmentReplacer = strings.NewReplacer(
	"%", "%25",
	"/", "%2F",
	"\\", "%5C",
	":", "%3A",
)

func sanitizePathSegment(segment string) string {
	return pathSegmentReplacer.Replace(segment)
}

var ValidRunID = regexp.MustCompile(`^[0-9]+-[0-9a-f]{8}$`)

func NewID() string {
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(randomBytes))
}
