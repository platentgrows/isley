#!/usr/bin/env bash
#
# sync-isley-upstream.sh
#
# Merge an upstream dwot/isley release into your fork's local-self-hosted
# branch, and catch migration-number collisions before they bite.
#
# Usage:
#   ./sync-isley-upstream.sh [TAG] [--dry-run] [--renumber]
#
#   TAG          release tag to merge (e.g. v2.6.0). Default: newest v* tag.
#   --dry-run    fetch and report (new commits, new migrations, collisions)
#                without changing anything.
#   --renumber   if your fork-only migrations collide with upstream's numbers,
#                renumber them to the next free number (git mv, both drivers)
#                and commit, then merge.
#
# Environment overrides:
#   BRANCH            fork branch to merge into   (default: local-self-hosted)
#   UPSTREAM_REMOTE   remote name for dwot/isley  (default: upstream)
#   UPSTREAM_URL      URL used if the remote is missing
#
# Nothing is pushed. A backup branch is created before any change.

set -euo pipefail

BRANCH="${BRANCH:-local-self-hosted}"
UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_URL="${UPSTREAM_URL:-https://github.com/dwot/isley.git}"
MIGRATION_DIRS=(model/migrations/postgres model/migrations/sqlite)

TARGET=""
RENUMBER=0
DRY_RUN=0

usage() { sed -n '3,22p' "$0" | sed 's/^# \{0,1\}//'; }
die()   { echo "ERROR: $*" >&2; exit 1; }
info()  { echo "==> $*"; }

for arg in "$@"; do
  case "$arg" in
    --renumber) RENUMBER=1 ;;
    --dry-run)  DRY_RUN=1 ;;
    -h|--help)  usage; exit 0 ;;
    -*)         die "unknown option: $arg" ;;
    *)          TARGET="$arg" ;;
  esac
done

# ---------------------------------------------------------------- helpers ---

# "<numeric prefix> <stem>" for each migration in DIR at REF, e.g.
#   022 022_smoke_report.postgres
list_stems() {
  local ref=$1 dir=$2
  git ls-tree --name-only "$ref" "$dir/" 2>/dev/null \
    | sed 's|.*/||' \
    | sed -E 's/\.(up|down)\.sql$//' \
    | grep -E '^[0-9]+_' \
    | sort -u \
    | awk -F_ '{print $1, $0}' || true
}

# Fork-only migrations whose number is already used by a different upstream
# migration. Prints "<prefix> <stem>".
collisions() {
  local dir=$1 up loc p stem
  up=$(list_stems "$TARGET" "$dir")
  loc=$(list_stems "$BRANCH" "$dir")
  while read -r p stem; do
    [ -n "$p" ] || continue
    if ! grep -qxF "$p $stem" <<<"$up" && grep -q "^$p " <<<"$up"; then
      echo "$p $stem"
    fi
  done <<<"$loc"
}

# Highest migration number across both sides and both drivers.
max_prefix() {
  local m=0 dir ref p n
  for dir in "${MIGRATION_DIRS[@]}"; do
    for ref in "$TARGET" "$BRANCH"; do
      while read -r p _; do
        [ -n "$p" ] || continue
        n=$((10#$p))
        if [ "$n" -gt "$m" ]; then m=$n; fi
      done < <(list_stems "$ref" "$dir")
    done
  done
  echo "$m"
}

# Duplicate migration numbers in the working tree.
duplicate_prefixes() {
  local dir=$1
  git ls-files "$dir" \
    | sed 's|.*/||' \
    | sed -E 's/\.(up|down)\.sql$//' \
    | grep -E '^[0-9]+_' \
    | sort -u \
    | awk -F_ '{print $1}' \
    | sort | uniq -d || true
}

# ------------------------------------------------------------- preflight ---

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not inside a git repo"
cd "$(git rev-parse --show-toplevel)"

if ! git diff --quiet || ! git diff --cached --quiet; then
  die "working tree has uncommitted changes; commit or stash them first"
fi

git rev-parse -q --verify "refs/heads/$BRANCH" >/dev/null \
  || die "branch '$BRANCH' not found"

if ! git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  info "Adding remote '$UPSTREAM_REMOTE' -> $UPSTREAM_URL"
  git remote add "$UPSTREAM_REMOTE" "$UPSTREAM_URL"
fi

info "Fetching $UPSTREAM_REMOTE (with tags)"
git fetch "$UPSTREAM_REMOTE" --tags --prune

if [ -z "$TARGET" ]; then
  TARGET=$(git tag --list 'v[0-9]*' --sort=-v:refname | head -n1)
  [ -n "$TARGET" ] || die "no v* tags found after fetch"
  info "No tag given; using newest: $TARGET"
fi
if ! git rev-parse -q --verify "refs/tags/$TARGET^{commit}" >/dev/null; then
  if git rev-parse -q --verify "refs/tags/v$TARGET^{commit}" >/dev/null; then
    TARGET="v$TARGET"
  else
    die "tag '$TARGET' not found"
  fi
fi

if git merge-base --is-ancestor "$TARGET" "$BRANCH"; then
  info "$BRANCH already contains $TARGET. Nothing to do."
  exit 0
fi

# ---------------------------------------------------------------- report ---

echo
info "Upstream commits in $TARGET not yet in $BRANCH:"
git log --oneline "$BRANCH..$TARGET" | head -n 30

echo
info "Migrations upstream added or changed since your last sync:"
git diff --name-status "$BRANCH...$TARGET" -- "${MIGRATION_DIRS[@]}" \
  | grep -E '\.sql$' || echo "  (none)"

COLLIDING=$(for d in "${MIGRATION_DIRS[@]}"; do collisions "$d"; done \
  | awk '{print $1}' | sort -u)

echo
if [ -n "$COLLIDING" ]; then
  info "MIGRATION NUMBER COLLISIONS (fork-only migrations sharing a number with upstream):"
  for d in "${MIGRATION_DIRS[@]}"; do
    collisions "$d" | sed "s|^|  $d: |"
  done
else
  info "No migration number collisions."
fi

if [ "$DRY_RUN" -eq 1 ]; then
  echo
  info "Dry run: nothing changed."
  exit 0
fi

if [ -n "$COLLIDING" ] && [ "$RENUMBER" -ne 1 ]; then
  echo
  die "collisions found. Re-run with --renumber to renumber your fork's migrations automatically."
fi

# ------------------------------------------------------------- do the work ---

git checkout "$BRANCH"
BACKUP="backup/${BRANCH}-before-${TARGET}"
git branch -f "$BACKUP" "$BRANCH"
info "Backup branch: $BACKUP   (undo with: git reset --hard $BACKUP)"

RENUMBERED=()
if [ -n "$COLLIDING" ]; then
  next=$(( $(max_prefix) + 1 ))
  for old in $COLLIDING; do
    new=$(printf '%03d' "$next")
    next=$((next + 1))
    for dir in "${MIGRATION_DIRS[@]}"; do
      while read -r stem; do
        [ -n "$stem" ] || continue
        for kind in up down; do
          f="$dir/$stem.$kind.sql"
          if git cat-file -e "$BRANCH:$f" 2>/dev/null; then
            git mv "$f" "$dir/${new}_${stem#*_}.$kind.sql"
          fi
        done
      done < <(collisions "$dir" | awk -v p="$old" '$1==p {print $2}')
    done
    RENUMBERED+=("$old:$new")
  done
  git commit -q -m "Renumber fork migrations after upstream $TARGET"
  info "Renumbered fork migrations: ${RENUMBERED[*]}  (old:new)"
fi

PRE=$(git rev-parse HEAD)
info "Merging $TARGET into $BRANCH"
if ! git merge --no-ff "$TARGET" -m "Merge isley $TARGET into $BRANCH"; then
  echo
  echo "Merge conflicts. Resolve them, then 'git commit'. To bail out:"
  echo "  git merge --abort   (or: git reset --hard $BACKUP)"
  exit 1
fi

# --------------------------------------------------------------- verify ---

for d in "${MIGRATION_DIRS[@]}"; do
  dups=$(duplicate_prefixes "$d")
  if [ -n "$dups" ]; then
    echo "Duplicate migration numbers in $d: $dups" >&2
    die "fix before deploying (undo with: git reset --hard $BACKUP)"
  fi
done

if [ -f go.mod ] && command -v go >/dev/null 2>&1; then
  info "go build ./..."
  go build ./...
fi

echo
info "Migrations added by this merge:"
git diff --name-status "$PRE" HEAD -- "${MIGRATION_DIRS[@]}" | grep -E '\.sql$' || echo "  (none)"

# --------------------------------------------------- deploy-time guidance ---

if [ "${#RENUMBERED[@]}" -gt 0 ]; then
  min_old=999999
  for pair in "${RENUMBERED[@]}"; do
    o=$((10#${pair%%:*}))
    if [ "$o" -lt "$min_old" ]; then min_old=$o; fi
  done

  echo
  echo "------------------------------------------------------------------"
  echo " DATABASE NOTE: your fork migration(s) were renumbered ($(IFS=,; echo "${RENUMBERED[*]}"))"
  echo "------------------------------------------------------------------"
  echo " A DB that already ran your fork migration has the OLD number"
  echo " recorded. Deploying as-is skips upstream's migrations at that number"
  echo " and fails on yours (the 'Dirty database version' error)."
  echo
  echo " Before deploying, on each DB (back it up first):"
  echo "   SELECT version, dirty FROM schema_migrations;"
  echo
  echo " If version = $min_old (your old number) and dirty = false, run:"
  echo "   UPDATE schema_migrations SET version = $((min_old - 1)), dirty = false;"
  echo " so the migrator runs upstream's new migrations first, then yours."
  echo
  echo " That re-runs your migration on a DB that already has its changes, so"
  echo " it must be idempotent (IF NOT EXISTS / IF EXISTS):"
  for pair in "${RENUMBERED[@]}"; do
    new=${pair##*:}
    for f in model/migrations/postgres/${new}_*.up.sql; do
      [ -e "$f" ] || continue
      if grep -qiE 'if (not )?exists' "$f"; then
        echo "   ok:   $f"
      else
        echo "   WARN: $f has no IF [NOT] EXISTS guard; add one before using the reset above"
      fi
    done
  done
  echo
  echo " If the DB is dirty already, fix it by hand as before (apply the missing"
  echo " upstream migration, then set version to the highest number, dirty = false)."
fi

echo
info "Done. Review with 'git log --oneline -n 10', then push and deploy:"
echo "   git push origin $BRANCH"
