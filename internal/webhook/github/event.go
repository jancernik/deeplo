package github

import (
	"encoding/json"
	"fmt"
	"strings"
)

type PushPayload struct {
	Ref        string     `json:"ref"`
	After      string     `json:"after"`
	Repository Repository `json:"repository"`
	Commits    []Commit   `json:"commits"`
}

type Repository struct {
	FullName string `json:"full_name"`
}

type Commit struct {
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

func ParsePushPayload(data []byte) (*PushPayload, error) {
	var p PushPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse push payload: %w", err)
	}
	return &p, nil
}

func (payload *PushPayload) Branch() string {
	return strings.TrimPrefix(payload.Ref, "refs/heads/")
}

func (payload *PushPayload) ChangedFiles() []string {
	seen := make(map[string]struct{})
	var files []string
	add := func(paths []string) {
		for _, path := range paths {
			if _, ok := seen[path]; !ok {
				seen[path] = struct{}{}
				files = append(files, path)
			}
		}
	}
	for _, c := range payload.Commits {
		add(c.Added)
		add(c.Modified)
		add(c.Removed)
	}
	return files
}
