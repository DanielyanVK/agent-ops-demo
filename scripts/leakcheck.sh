#!/usr/bin/env bash
# Fails when anything under DIR (default: runs) still carries a name, path or
# field the sanitizer must have removed. Run it before publishing runs.
set -euo pipefail

dir=${1:-runs}
[ -d "$dir" ] || { echo "каталог не найден: $dir" >&2; exit 2; }

login=$(id -un)
host=$(hostname)
full=$(id -F 2>/dev/null || getent passwd "$login" 2>/dev/null | cut -d: -f5 | cut -d, -f1 || true)
gitname=$(git config user.name 2>/dev/null || true)
gitemail=$(git config user.email 2>/dev/null || true)

# Names match as whole words, so that a login "max" does not flag
# error_max_turns; paths and field names match anywhere. Values this short are
# skipped, as the sanitizer skips them: they would match everywhere.
words=()
for p in "$login" "$host" "${host%%.*}" "$full" "$gitname"; do
  if [ ${#p} -ge 3 ]; then words+=(-e "$p"); fi
done
parts=()
for p in "$HOME" "$gitemail" /Users/ /home/ /private/ /var/folders var-folders /tmp/claude- \
  session_id request_id rate_limit credential '"plugins"' '"tools"'; do
  if [ ${#p} -ge 3 ]; then parts+=(-e "$p"); fi
done

# grep exits 1 when nothing matches; only 2 is an error.
found() { grep -rilF "$@" -- "$dir" || [ $? -eq 1 ]; }
files=$({
  if [ ${#words[@]} -gt 0 ]; then found -w "${words[@]}"; fi
  found "${parts[@]}"
} | sort -u)

if [ -n "$files" ]; then
  echo "возможные утечки в файлах:"
  echo "$files"
  exit 1
fi
echo "утечек не найдено"
