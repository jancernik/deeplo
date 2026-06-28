<div align="center">

<img src="website/public/icons/256x256.png" alt="deeplo" width="112" height="112" />

# deeplo

**A small, agentless deployment tool for Docker Compose over SSH.**

[Documentation](https://deeplo.xyz) · [Native install](https://deeplo.xyz/guides/native-install) · [Docker image](https://deeplo.xyz/guides/docker-image) · [CLI reference](https://deeplo.xyz/reference/cli-reference)

</div>

---

A small deploy runner for Docker Compose projects on remote hosts. One binary runs as the daemon and CLI, watches git repos for new commits, works out which projects changed, then runs Docker Compose on each target over SSH.

There are no agents on target host and your deployment setup is defined in on config file: hosts, repos, projects, and the paths that should trigger each deploy.

```yaml
hosts:
  - name: web-1
    address: 10.0.0.10
    deploy_dir: /srv/apps

repos:
  - name: myapp
    url: git@github.com:yourorg/myapp.git
    branch: main
    trigger_mode: hybrid

projects:
  - name: myapp
    repo: myapp
    repo_subdir: deploy
    targets:
      - web-1
    watch_paths:
      - deploy/**
    persist_files:
      - .env
```
