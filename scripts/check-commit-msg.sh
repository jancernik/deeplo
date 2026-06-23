#!/bin/sh

# Validate that a commit message follows Conventional Commits.

set -eu

message_file="$1"

# The subject is the first line that is neither blank nor a comment.
subject="$(grep -v '^#' "$message_file" | sed '/^[[:space:]]*$/d' | head -n 1)"

[ -z "$subject" ] && exit 0

case "$subject" in
  "Merge "* | "Revert "* | "fixup! "* | "squash! "*)
    exit 0
    ;;
esac

# <type>(<optional scope>)<optional !>: <summary>
pattern='^(feat|fix|perf|refactor|docs|test|build|ci|chore)(\([a-z0-9._/-]+\))?!?: .+'

if printf '%s' "$subject" | grep -Eq "$pattern"; then
  exit 0
fi

cat >&2 <<EOF
Commit message does not follow Conventional Commits.

Got:     $subject

Format:  <type>(<optional scope>): <summary>
Types:   feat fix perf refactor docs test build ci chore
EOF
exit 1
