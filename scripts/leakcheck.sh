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
  session_id request_id rate_limit credential auth.json '"plugins"' '"tools"'; do
  if [ ${#p} -ge 3 ]; then parts+=(-e "$p"); fi
done

# Secrets, as regular expressions. AGENTOPS_LEAK_EXTRA adds your own names that
# the checks above cannot know (a machine, an employer), also a regular
# expression: AGENTOPS_LEAK_EXTRA="acme|corp-vpn[0-9]+". Matched in any case.
re='sk-[A-Za-z0-9]{16,}|BEGIN [A-Z ]*PRIVATE KEY|Bearer [A-Za-z0-9._-]{16,}'
if [ -n "${AGENTOPS_LEAK_EXTRA:-}" ]; then
  extra=$AGENTOPS_LEAK_EXTRA
  # An empty alternative matches everything; a broken expression would make grep fail.
  rc=0
  printf '' | grep -E -e "($extra)" >/dev/null 2>&1 || rc=$?
  if [[ "$extra" == \|* || "$extra" == *\| || "$extra" == *\|\|* || "$extra" == *\(\|* || "$extra" == *\|\)* ]] || [ "$rc" -ge 2 ]; then
    echo "AGENTOPS_LEAK_EXTRA: неверное регулярное выражение" >&2
    exit 2
  fi
  re="$re|($extra)"
fi

# grep exits 1 when nothing matches; 2 or more is an error and must not read as "clean".
hits=$(mktemp)
trap 'rm -f "$hits"' EXIT
scan() {
  local rc=0
  grep -ril "$@" -- "$dir" >>"$hits" || rc=$?
  if [ "$rc" -ge 2 ]; then
    echo "ошибка grep (код $rc), проверка не выполнена" >&2
    exit 2
  fi
}
scan -E -e "$re"
if [ ${#words[@]} -gt 0 ]; then scan -F -w "${words[@]}"; fi
scan -F "${parts[@]}"
files=$(sort -u "$hits")

if [ -n "$files" ]; then
  echo "возможные утечки в файлах:"
  echo "$files"
  exit 1
fi
echo "утечек не найдено"
