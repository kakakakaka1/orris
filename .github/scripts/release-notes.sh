#!/usr/bin/env bash
#
# Generate release notes for a tag from the commits since the previous tag.
#
# Usage:
#   release-notes.sh <tag> <repo> [github|telegram]
#
# The two formats differ on purpose: the GitHub body is full Markdown, while the
# Telegram one is plain text with the handful of HTML tags Telegram accepts.
# Feeding the Markdown version to Telegram renders headings and <details> as
# literal noise, so they are generated separately rather than post-processed.
#
# Commits are grouped by their Conventional Commit type, most significant first.
# Anything that does not parse as a Conventional Commit still shows up, under
# "Other", so a stray commit is never silently dropped from the notes.
#
# A commit can contribute an operator-facing upgrade note by starting a line in
# its body with "UPGRADE NOTE:". The note runs to the next blank line, so it can
# span several lines:
#
#     UPGRADE NOTE: Deployments behind a public-IP reverse proxy must set
#     ORRIS_SERVER_TRUSTED_PROXIES to that proxy's address.
#
set -euo pipefail

CURRENT_TAG="${1:?usage: release-notes.sh <tag> <repo> [github|telegram]}"
REPO="${2:?usage: release-notes.sh <tag> <repo> [github|telegram]}"
FORMAT="${3:-github}"

# Unit separator: safe against any character a commit subject may contain.
US=$'\x1f'
# Sentinel marking the end of one commit body when streaming bodies line by line.
SENTINEL='@@ORRIS-COMMIT-END@@'

PREV_TAG="$(git describe --tags --abbrev=0 "${CURRENT_TAG}^" 2>/dev/null || true)"
if [ -n "$PREV_TAG" ]; then
    RANGE="${PREV_TAG}..${CURRENT_TAG}"
else
    RANGE="$CURRENT_TAG"
fi

# strip_type removes the Conventional Commit prefix so the bullet reads as prose.
strip_type() {
    sed -E 's/^[a-zA-Z]+(\([^)]*\))?!?:[[:space:]]*//'
}

# knownTypes matches any subject that parses as a Conventional Commit whose type
# gets its own section below. Written with a character class rather than an
# escaped group because awk's ERE engine rejects "\(" on some platforms.
KNOWN_TYPES='^(feat|fix|perf|refactor|docs|style|chore|test|build|ci)[(!:]'

# EXCLUDE_SHAS holds short SHAs already reported under "Breaking changes", so a
# breaking commit is listed once rather than again under its own type.
EXCLUDE_SHAS=""

# commits_matching prints "<subject>|<short-sha>" for commits whose subject
# matches the given extended regex. Passing "!" as the second argument inverts
# the match, which is how the catch-all "Other" section is collected.
commits_matching() {
    local regex="$1" negate="${2:-}"
    git log "$RANGE" --no-merges --pretty=format:"%s${US}%h" \
        | awk -F"$US" -v re="$regex" -v neg="$negate" -v excl="$EXCLUDE_SHAS" '
            BEGIN {
                n = split(excl, arr, " ")
                for (i = 1; i <= n; i++) if (arr[i] != "") skip[arr[i]] = 1
            }
            {
                if ($2 in skip) next
                matched = ($1 ~ re)
                if (neg != "") matched = !matched
                if (matched) print $1 "|" $2
            }'
}

# breaking_commits lists commits marked with "!" before the colon in the subject
# or carrying a BREAKING CHANGE trailer in the body. The two are collected
# separately and de-duplicated, because matching a body trailer and a subject
# pattern in one pass would need multi-line records, and awk's RS handling for
# NUL-delimited input is not portable.
breaking_commits() {
    {
        git log "$RANGE" --no-merges --pretty=format:"%s${US}%h" \
            | awk -F"$US" '$1 ~ /^[a-zA-Z]+[^:]*!:/ { print $1 "|" $2 }'
        printf '\n'
        git log "$RANGE" --no-merges --grep='BREAKING CHANGE' --pretty=format:"%s|%h"
        printf '\n'
    } | grep -v '^[[:space:]]*$' | awk '!seen[$0]++' || true
    # "|| true": grep exits 1 when a release has no breaking changes, which under
    # `set -e` plus `set -o pipefail` would abort the whole script.
}

# upgrade_notes extracts operator-facing notes flagged with "UPGRADE NOTE:".
# A note ends at the first blank line, so the rest of the commit body stays out.
# Commits are separated by an explicit sentinel line rather than by a record
# separator so that the parser is plain line-at-a-time and portable across awks.
upgrade_notes() {
    git log "$RANGE" --no-merges --reverse --pretty=format:"%b%n${SENTINEL}" \
        | awk -v sentinel="$SENTINEL" '
            $0 == sentinel { collecting = 0; next }
            /^UPGRADE NOTE:/ {
                line = $0
                sub(/^UPGRADE NOTE:[[:space:]]*/, "", line)
                collecting = 1
                if (line != "") print "FIRST" line
                next
            }
            collecting && /^[[:space:]]*$/ { collecting = 0; next }
            collecting { print "CONT" $0 }
        '
}

has_upgrade_notes() {
    [ -n "$(upgrade_notes)" ]
}

# ---------------------------------------------------------------------------
# GitHub Markdown
# ---------------------------------------------------------------------------
render_github_section() {
    local heading="$1" regex="$2" negate="${3:-}" entries
    entries="$(commits_matching "$regex" "$negate")"
    [ -z "$entries" ] && return 0

    printf '### %s\n\n' "$heading"
    while IFS='|' read -r subject sha; do
        [ -z "$subject" ] && continue
        printf -- '- %s (%s)\n' "$(printf '%s' "$subject" | strip_type)" "$sha"
    done <<< "$entries"
    printf '\n'
}

render_github() {
    local breaking
    breaking="$(breaking_commits)"
    if [ -n "$breaking" ]; then
        printf '### Breaking changes\n\n'
        while IFS='|' read -r subject sha; do
            [ -z "$subject" ] && continue
            printf -- '- %s (%s)\n' "$(printf '%s' "$subject" | strip_type)" "$sha"
            EXCLUDE_SHAS="$EXCLUDE_SHAS $sha"
        done <<< "$breaking"
        printf '\n'
    fi

    render_github_section 'Features'      '^feat[(!:]'
    render_github_section 'Fixes'         '^fix[(!:]'
    render_github_section 'Performance'   '^perf[(!:]'
    render_github_section 'Refactoring'   '^refactor[(!:]'
    render_github_section 'Documentation' '^docs[(!:]'
    render_github_section 'Maintenance'   '^(style|chore|test|build|ci)[(!:]'
    render_github_section 'Other'         "$KNOWN_TYPES" '!'

    if has_upgrade_notes; then
        printf '### Upgrade notes\n\n'
        upgrade_notes | sed -e 's/^FIRST/- /' -e 's/^CONT/  /'
        printf '\n'
    fi

    cat <<'INSTALL'
### Install

```bash
curl -fsSL https://raw.githubusercontent.com/orris-inc/orris/main/install.sh | bash
```

### Update

```bash
# From your Orris installation directory
./install.sh update
```

INSTALL

    printf '### Docker image\n\n```\nghcr.io/%s:%s\n```\n\n' "$REPO" "${CURRENT_TAG#v}"

    if [ -n "$PREV_TAG" ]; then
        printf '**Full changelog**: https://github.com/%s/compare/%s...%s\n\n' \
            "$REPO" "$PREV_TAG" "$CURRENT_TAG"
    fi

    printf '<details>\n<summary>Full commit messages</summary>\n\n'
    git log "$RANGE" --no-merges --reverse --pretty=format:'#### %s%n%n%b'
    printf '\n\n</details>\n'
}

# ---------------------------------------------------------------------------
# Telegram (plain text, minimal HTML)
# ---------------------------------------------------------------------------
escape_html() {
    sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g'
}

render_telegram_section() {
    local heading="$1" regex="$2" negate="${3:-}" entries
    entries="$(commits_matching "$regex" "$negate")"
    [ -z "$entries" ] && return 0

    printf '<b>%s</b>\n' "$heading"
    while IFS='|' read -r subject sha; do
        [ -z "$subject" ] && continue
        printf -- '• %s\n' "$(printf '%s' "$subject" | strip_type | escape_html)"
    done <<< "$entries"
    printf '\n'
}

render_telegram() {
    local breaking
    breaking="$(breaking_commits)"
    if [ -n "$breaking" ]; then
        printf '<b>Breaking changes</b>\n'
        while IFS='|' read -r subject sha; do
            [ -z "$subject" ] && continue
            printf -- '• %s\n' "$(printf '%s' "$subject" | strip_type | escape_html)"
            EXCLUDE_SHAS="$EXCLUDE_SHAS $sha"
        done <<< "$breaking"
        printf '\n'
    fi

    render_telegram_section 'Features'      '^feat[(!:]'
    render_telegram_section 'Fixes'         '^fix[(!:]'
    render_telegram_section 'Performance'   '^perf[(!:]'
    render_telegram_section 'Refactoring'   '^refactor[(!:]'
    render_telegram_section 'Documentation' '^docs[(!:]'
    render_telegram_section 'Maintenance'   '^(style|chore|test|build|ci)[(!:]'
    render_telegram_section 'Other'         "$KNOWN_TYPES" '!'

    if has_upgrade_notes; then
        printf '<b>Upgrade notes</b>\n'
        upgrade_notes | escape_html | sed -e 's/^FIRST/• /' -e 's/^CONT/  /'
        printf '\n'
    fi
}

case "$FORMAT" in
    github)   render_github ;;
    telegram) render_telegram ;;
    *)        echo "unknown format: $FORMAT" >&2; exit 1 ;;
esac
