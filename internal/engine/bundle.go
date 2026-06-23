package engine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/utils"
)

// Extracts the compose files and extra files for a project and returns a bundle ready for upload.
// ExtraFiles can be individual files or directories, directories are expanded recursively.
func buildBundle(ctx context.Context, repo *mirror.Repo, sha string, project config.Project, tmpDir string) (*compose.Bundle, error) {
	var files []compose.BundleFile
	for _, name := range slices.Concat(project.ComposeFiles, project.ExtraFiles) {
		gitPath := path.Join(project.RepoSubdir, name)
		paths, err := repo.ListFiles(ctx, sha, gitPath)
		if err != nil {
			return nil, fmt.Errorf("list %s@%s: %w", gitPath, utils.ShortSha(sha), err)
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("%s not found at %s", gitPath, utils.ShortSha(sha))
		}
		for _, filePath := range paths {
			content, err := repo.ReadFile(ctx, sha, filePath)
			if err != nil {
				return nil, fmt.Errorf("read %s@%s: %w", filePath, utils.ShortSha(sha), err)
			}
			remoteName := strings.TrimPrefix(filePath, project.RepoSubdir+"/")
			localPath := filepath.Join(tmpDir, remoteName)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return nil, fmt.Errorf("mkdir for %s: %w", remoteName, err)
			}
			if err := os.WriteFile(localPath, content, 0644); err != nil {
				return nil, fmt.Errorf("write %s: %w", remoteName, err)
			}
			files = append(files, compose.BundleFile{LocalPath: localPath, RemoteName: remoteName})
		}
	}
	return &compose.Bundle{Files: files}, nil
}
