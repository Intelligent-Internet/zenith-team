# Shared naming rules for the git hooks in this folder (sourced, not run).
#
# Commit subjects follow Conventional Commits 1.0 with the standard type list
# (the one commitlint's config-conventional uses):
#   type(scope)!: description
# Branch names use the same types: type/short-name, lowercase words joined by
# hyphens, dots only inside versions (release/1.4.0).

TYPES="build chore ci docs feat fix perf refactor revert style test"
BRANCH_TYPES="$TYPES hotfix release"
MAX_SUBJECT=72

types_re() {
  printf '%s' "$1" | tr ' ' '|'
}

# check_subject SUBJECT: prints why and returns 1 when SUBJECT breaks the rules.
check_subject() {
  case "$1" in
    "Merge "* | "Revert \""* | "fixup! "* | "squash! "* | "amend! "*) return 0 ;;
  esac
  if ! printf '%s\n' "$1" | grep -Eq "^($(types_re "$TYPES"))(\([a-z0-9][a-z0-9._/-]*\))?!?: [^ ].*[^. ]$"; then
    echo "Use a Conventional Commit subject: type(scope): description" >&2
    echo "  feat(tasks): let members reorder task dependencies" >&2
    echo "  fix(daemon): retry the first claim" >&2
    echo "  feat(web)!: drop the old share links    (! marks a breaking change)" >&2
    echo "Types: $TYPES. Lowercase type and scope, no trailing period." >&2
    echo "Got: $1" >&2
    return 1
  fi
  if [ "${#1}" -gt "$MAX_SUBJECT" ]; then
    echo "Keep the subject to $MAX_SUBJECT characters; it has ${#1}: $1" >&2
    return 1
  fi
}

# check_branch NAME: prints why and returns 1 when NAME breaks the rules.
check_branch() {
  case "$1" in
    main | dependabot/*) return 0 ;;
  esac
  if ! printf '%s\n' "$1" | grep -Eq "^($(types_re "$BRANCH_TYPES"))/[a-z0-9]+([.-][a-z0-9]+)*$"; then
    echo "Name branches type/short-name in lowercase, words joined by hyphens:" >&2
    echo "  feat/task-reorder   fix/daemon-first-claim   docs/self-hosting-tls" >&2
    echo "Types: $BRANCH_TYPES." >&2
    echo "Got: $1  (rename with: git branch -m <new-name>)" >&2
    return 1
  fi
}
