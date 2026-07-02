#!/bin/sh
set -eu

# deeplo installer - Linux only

# Usage:
#   curl -fsSL https://deeplo.xyz/install.sh | sh
#   sh install.sh [--build] [--version <tag>]

# Runs as the current user. Prompts for sudo for privileged steps.
# To update or remove an existing install, use: deeplo update / deeplo uninstall

# Environment overrides:
#   DEEPLO_VERSION        install this version instead of latest (e.g. "v1.2.3")
#   DEEPLO_LOCAL_BIN      path to directory containing a local deeplo binary (skip download)
#   DEEPLO_SOURCE_DIR     source directory for --build mode (default: current directory)

GITHUB_REPO="jancernik/deeplo"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/deeplo"
DATA_DIR="/var/lib/deeplo"
KEYS_DIR="/etc/deeplo/keys"
SERVICE_USER="deeplo"
SERVICE_GROUP="deeplo"
UNIT_NAME="deeplo"
UNIT_FILE="/etc/systemd/system/${UNIT_NAME}.service"
CONFIG_FILE="${CONFIG_DIR}/config.yml"
ENV_FILE="${CONFIG_DIR}/deeplo.env"
DEPLOY_KEY_FILE="${KEYS_DIR}/deploy_key"

# output helpers

info() { printf '%s\n' "$*" >&2; }
success() { printf '\033[1;32m✓\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31m%s\033[0m\n' "$*" >&2
	exit 1
}

# cleanup

_CLEANUP_DIRS=""
_SUDO_KEEPALIVE_PID=""

_cleanup() {
	if [ -n "$_SUDO_KEEPALIVE_PID" ]; then
		kill "$_SUDO_KEEPALIVE_PID" 2>/dev/null || true
	fi
	for dir in $_CLEANUP_DIRS; do
		rm -rf "$dir"
	done
}

trap '_cleanup' EXIT

# system checks

preflight() {
	missing=""
	for tool in curl ssh-keygen install tee useradd groupadd systemctl; do
		command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
	done
	if [ -n "$missing" ]; then
		die "Required tools not found:$missing"
	fi
}

require_systemd() {
	command -v systemctl >/dev/null 2>&1 ||
		die "systemctl not found - deeplo requires a systemd-based Linux system"
	[ -d /run/systemd/system ] ||
		die "systemd does not appear to be the active init system (missing /run/systemd/system)"
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64) echo "amd64" ;;
	aarch64) echo "arm64" ;;
	*) die "Unsupported architecture: $arch (supported: x86_64, aarch64)" ;;
	esac
}

detect_os() {
	os=$(uname -s)
	[ "$os" = "Linux" ] || die "deeplo requires Linux (got: $os)"
	echo "linux"
}

# privilege helpers

run_root() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	else
		sudo "$@"
	fi
}

require_sudo() {
	[ "$(id -u)" -eq 0 ] && return 0

	command -v sudo >/dev/null 2>&1 ||
		die "sudo is required for privileged steps but was not found"

	info Requesting sudo access...
	sudo -v || die "sudo authentication failed"

	(while true; do
		sudo -n true 2>/dev/null
		sleep 50
	done) &
	_SUDO_KEEPALIVE_PID=$!
}

# helpers

latest_version() {
	version=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" |
		grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
	[ -n "$version" ] || die "Could not determine latest version from GitHub API"
	echo "$version"
}

download_binary() {
	version="$1"
	arch="$2"
	case "$version" in
	v*) ;;
	*) version="v${version}" ;;
	esac
	base_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"
	tmp_dir=$(mktemp -d)
	_CLEANUP_DIRS="$_CLEANUP_DIRS $tmp_dir"

	info "Downloading deeplo ${version#v} (linux/${arch})..."
	url="${base_url}/deeplo_linux_${arch}"
	curl -fsSL --progress-bar -o "${tmp_dir}/deeplo" "$url" ||
		die "Failed to download ${url}"
	chmod +x "${tmp_dir}/deeplo"

	echo "$tmp_dir"
}

resolve_build_version() {
	src_dir="${DEEPLO_SOURCE_DIR:-$(pwd)}"
	git -C "$src_dir" rev-parse --short HEAD 2>/dev/null || echo "dev"
}

build_from_source() {
	version="$1"
	src_dir="${DEEPLO_SOURCE_DIR:-$(pwd)}"

	[ -f "${src_dir}/go.mod" ] ||
		die "--build requires a Go source tree; go.mod not found in: ${src_dir}
  Set DEEPLO_SOURCE_DIR or run from the repo root"

	command -v go >/dev/null 2>&1 ||
		die "--build requires a Go toolchain; 'go' not found in PATH
  Install Go from https://go.dev/dl/ or remove --build to download a release binary"

	tmp_dir=$(mktemp -d)
	_CLEANUP_DIRS="$_CLEANUP_DIRS $tmp_dir"

	info "Building deeplo from source (${src_dir})..."
	(cd "$src_dir" && go build -trimpath \
		-ldflags="-X github.com/jancernik/deeplo/internal/build.Version=${version}" \
		-o "${tmp_dir}/deeplo" ./cmd/deeplo) ||
		die "go build failed - check the output above for details"
	chmod +x "${tmp_dir}/deeplo"

	success "Built deeplo binary"
	echo "$tmp_dir"
}

# privileged helpers

_install_binary() {
	src_dir="$1"
	run_root install -m 755 "${src_dir}/deeplo" "${INSTALL_DIR}/deeplo"
	success "Installed binary to ${INSTALL_DIR}/deeplo"
}

_install_unit() {
	run_root tee "$UNIT_FILE" >/dev/null <<EOF
[Unit]
Description=deeplo daemon service
After=network.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_GROUP}
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_DIR}/deeplo daemon
Restart=on-failure
RestartSec=5s
TimeoutStopSec=120s
RuntimeDirectory=deeplo
RuntimeDirectoryMode=0750
AmbientCapabilities=CAP_CHOWN

[Install]
WantedBy=multi-user.target
EOF
	success "Installed systemd unit: ${UNIT_FILE}"
}

_install_completions() {
	deeplo="${INSTALL_DIR}/deeplo"
	installed=0

	if [ -d /usr/share/bash-completion/completions ] &&
		"$deeplo" completion bash 2>/dev/null |
		run_root tee /usr/share/bash-completion/completions/deeplo >/dev/null; then
		installed=1
	fi

	if [ -d /usr/share/zsh/site-functions ] &&
		"$deeplo" completion zsh 2>/dev/null |
		run_root tee /usr/share/zsh/site-functions/_deeplo >/dev/null; then
		installed=1
	fi

	if [ -d /usr/share/fish/vendor_completions.d ] &&
		"$deeplo" completion fish 2>/dev/null |
		run_root tee /usr/share/fish/vendor_completions.d/deeplo.fish >/dev/null; then
		installed=1
	fi

	if [ "$installed" -eq 1 ]; then
		success "Installed shell completions"
	fi
}

# install

do_install() {
	local_bin="${DEEPLO_LOCAL_BIN:-}"

	preflight
	detect_os >/dev/null
	arch=$(detect_arch)

	if [ -n "$local_bin" ]; then
		[ -f "${local_bin}/deeplo" ] ||
			die "DEEPLO_LOCAL_BIN=${local_bin} must contain a 'deeplo' binary"
		bin_dir="$local_bin"
		VERSION="local"
		info "Using local binary from ${local_bin}"
	elif [ "$BUILD_LOCAL" = "true" ]; then
		VERSION=$(resolve_build_version)
		bin_dir=$(build_from_source "$VERSION")
	else
		[ -z "$VERSION" ] && VERSION=$(latest_version)
		bin_dir=$(download_binary "$VERSION" "$arch")
	fi

	info "Installing deeplo ${VERSION#v}..."

	require_systemd
	require_sudo

	getent group "$SERVICE_GROUP" >/dev/null 2>&1 ||
		run_root groupadd --system "$SERVICE_GROUP"
	id "$SERVICE_USER" >/dev/null 2>&1 ||
		run_root useradd --system --no-create-home --shell /usr/sbin/nologin \
			--gid "$SERVICE_GROUP" --comment "deeplo deployment daemon" "$SERVICE_USER"

	operator="${SUDO_USER:-$(id -un)}"
	OPERATOR_GROUP=""
	if [ -n "$operator" ] && [ "$operator" != "root" ]; then
		OPERATOR_GROUP="$(id -gn "$operator" 2>/dev/null || true)"
	fi

	for dir in "$CONFIG_DIR" "$KEYS_DIR" "$DATA_DIR"; do
		[ -d "$dir" ] || run_root mkdir -p "$dir"
	done
	run_root chown "root:root" "$CONFIG_DIR"
	run_root chmod 755 "$CONFIG_DIR"
	run_root chown "root:${SERVICE_GROUP}" "$KEYS_DIR"
	run_root chmod 750 "$KEYS_DIR"
	run_root chown "${SERVICE_USER}:${SERVICE_GROUP}" "$DATA_DIR"
	run_root chmod 750 "$DATA_DIR"

	if ! run_root test -f "$DEPLOY_KEY_FILE"; then
		key_tmp=$(mktemp -d)
		_CLEANUP_DIRS="$_CLEANUP_DIRS $key_tmp"
		ssh-keygen -t ed25519 -C "deeplo-deploy-key" -N "" -f "${key_tmp}/deploy_key" -q
		run_root install -m 640 -o root -g "${SERVICE_GROUP}" \
			"${key_tmp}/deploy_key" "$DEPLOY_KEY_FILE"
		run_root install -m 644 -o root -g "${SERVICE_GROUP}" \
			"${key_tmp}/deploy_key.pub" "${DEPLOY_KEY_FILE}.pub"
		success "Generated deploy key"
	fi

	_install_binary "$bin_dir"

	_install_completions

	[ -f "$UNIT_FILE" ] || _install_unit

	if [ ! -f "$ENV_FILE" ]; then
		run_root tee "$ENV_FILE" >/dev/null <<EOF
# Full reference: https://deeplo.xyz/configuration/environment-variables
DEEPLO_DATA_DIR=${DATA_DIR}
DEEPLO_SSH_PRIVATE_KEY_FILE=${DEPLOY_KEY_FILE}
DEEPLO_ADMIN_GROUP=${OPERATOR_GROUP}

DEEPLO_SSH_USER=deploy
# DEEPLO_SSH_PORT=22

# DEEPLO_GITHUB_WEBHOOK_SECRET_FILE=${KEYS_DIR}/github_webhook_secret
# DEEPLO_GITHUB_TOKEN_FILE=${KEYS_DIR}/github_token

# DEEPLO_PUBLIC_URL=https://deeplo.example.com
# DEEPLO_LOG_SERVER=true
# DEEPLO_LOG_LEVEL=debug
EOF
		run_root chown "root:root" "$ENV_FILE"
		run_root chmod 644 "$ENV_FILE"
	fi

	if [ ! -f "$CONFIG_FILE" ]; then
		run_root tee "$CONFIG_FILE" >/dev/null <<'YAMLEOF'
# Define your hosts, repos, and projects below.
# Full schema: https://deeplo.xyz/configuration/config-file
#
# hosts:
#   - name: web-1
#     address: 10.0.0.10
#     deploy_dir: /srv/apps
#
# repos:
#   - name: myapp
#     url: git@github.com:yourorg/myapp.git
#     branch: main
#     trigger_mode: poll
#     poll_interval: 60s
#
# projects:
#   - name: myapp
#     repo: myapp
#     repo_subdir: deploy
#     targets:
#       - web-1
YAMLEOF
		run_root chown "root:root" "$CONFIG_FILE"
		run_root chmod 644 "$CONFIG_FILE"
	fi

	run_root systemctl daemon-reload

	printf '\n\033[1;32m✓ deeplo %s installed\033[0m\n\n' "${VERSION#v}"

	# A running service keeps the old binary in memory: 'install' swaps the file
	# on disk but never restarts the daemon. Tell the operator to restart instead
	# of printing first-install steps they have already done.
	if systemctl is-active --quiet "$UNIT_NAME"; then
		cat <<EOF
The deeplo service is already running an earlier build; the new binary won't
take effect until you restart it:

  deeplo service restart

Docs: https://deeplo.xyz
EOF
	else
		cat <<EOF
Next steps:

1. Configure         deeplo config edit
2. Validate          deeplo config check
3. Authorize key     deeplo authorize
4. Start             deeplo service enable --now
5. Verify            deeplo health

Docs: https://deeplo.xyz
EOF
	fi
}

# main

usage() {
	cat <<EOF
Usage:
  install.sh                      Install the latest release
  install.sh --build              Build from local source (requires Go toolchain)
  install.sh --version v1.2.3     Install a specific release

To update or remove an existing install, use the deeplo CLI:
  deeplo update [--version v1.2.3]
  deeplo uninstall [--purge]
EOF
}

BUILD_LOCAL=false
VERSION="${DEEPLO_VERSION:-}"

while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	--build)
		BUILD_LOCAL=true
		shift
		;;
	--version)
		[ $# -ge 2 ] || die "--version requires an argument (e.g. --version v1.2.3)"
		VERSION="$2"
		shift 2
		;;
	--version=*)
		VERSION="${1#--version=}"
		shift
		;;
	--*)
		die "Unknown option: $1

$(usage)"
		;;
	*)
		die "Unexpected argument: '$1'

$(usage)"
		;;
	esac
done

do_install
