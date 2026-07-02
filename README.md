<div align="center">

<img src="website/public/icons/256x256.png" alt="deeplo" width="112" height="112" />

# deeplo

**A small, agentless deployment tool for Docker Compose over SSH.**

[![Release](https://img.shields.io/github/v/release/jancernik/deeplo)](https://github.com/jancernik/deeplo/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/jancernik/deeplo/ci.yml?logo=github&label=CI)](https://github.com/jancernik/deeplo/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

---

A daemon watches your Git repositories and deploys Docker Compose projects to remote hosts over SSH. It maps repo paths to deployable projects, ships as one small binary, and does not install anything on the target hosts.

Hosts, repos, and projects live in one config file:

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
    persist_files:
      - .env
```

When a commit lands on a watched branch, deeplo detects it by webhook, polling, or both. It works out which projects changed, reads their Compose files from that exact commit, and updates each stack on its configured hosts.

## Getting started

- [Native install](https://deeplo.xyz/guides/native-install): run the daemon under systemd.
- [Docker image](https://deeplo.xyz/guides/docker-image): run the daemon as a container.

Full documentation is at [deeplo.xyz](https://deeplo.xyz).

## Highlights

- **Agentless**: targets need only Docker, Compose, and SSH access.
- **One small binary**: daemon and operator CLI in the same executable.
- **Push or poll**: webhook, poll, or hybrid triggers per repo.
- **Path-aware**: only deploys the projects whose files changed, monorepo friendly.
- **Traceable**: deployment state, history, and logs from the CLI.
- **Reporting**: GitHub deployment and commit statuses.
