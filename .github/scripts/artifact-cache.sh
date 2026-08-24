#!/usr/bin/env bash
#
# A build cache that survives between tags.
#
# GitHub scopes every actions/cache entry to the ref that created it and shares
# only the default branch's entries with other refs. This workflow runs on tags
# alone, so release/1.1 cannot read one byte of what release/1.0 cached, however
# the keys are written -- every release compiled from cold while still paying to
# upload an entry nothing would ever read. (It worked once, back when the
# commented-out `branches: "**"` trigger still built the default branch and left
# entries there; those expired seven days after that trigger was switched off.)
#
# Artifacts carry no ref scoping. So the cache travels as one.
#
#   artifact-cache.sh restore <artifact-name> <dest-dir>
#   artifact-cache.sh pack    <archive> <src-dir> [exclude-glob ...]
#
# restore never fails: a missing, expired or damaged cache is a slow build, not
# a broken release.

set -euo pipefail

usage() {
    echo "usage: $0 restore <artifact-name> <dest-dir>" >&2
    echo "       $0 pack <archive> <src-dir> [exclude-glob ...]" >&2
    exit 2
}

# Piped rather than `tar -I`, which is GNU-only: this script also runs under
# the bsdtar a developer may have. Object files and ccache entries are largely
# compressed already, so plain tar is an acceptable fallback -- the archive is
# read back by magic bytes, not by name, so either shape works.
tar_create() {
    local archive="$1"; shift
    if command -v zstd > /dev/null 2>&1; then
        tar -cf - "$@" | zstd -3 -T0 -c > "$archive"
    elif command -v gzip > /dev/null 2>&1; then
        tar -cf - "$@" | gzip -1 -c > "$archive"
    else
        tar -cf - "$@" > "$archive"
    fi
}

magic() {
    od -An -N4 -tx1 < "$1" | tr -d ' \n'
}

tar_extract() {
    local archive="$1" dest="$2"
    case "$(magic "$archive")" in
        28b52ffd*) zstd -dc "$archive" | tar -xf - -C "$dest" ;;
        1f8b*)     gzip -dc "$archive" | tar -xf - -C "$dest" ;;
        *)         tar -xf "$archive" -C "$dest" ;;
    esac
}

do_restore() {
    local name="$1" dest="$2" id archive
    [ -n "$name" ] && [ -n "$dest" ] || usage

    cold() {
        echo "::notice::Building without a warm ${name}: $1"
        rm -rf "$tmp"
        exit 0
    }

    mkdir -p "$dest"
    tmp="$(mktemp -d)"

    # The API returns every artifact that still carries this name, including
    # the ones from older runs, so pick the newest that has not expired.
    id="$(gh api "repos/${GITHUB_REPOSITORY}/actions/artifacts?name=${name}&per_page=100" \
          --jq '[.artifacts[] | select(.expired == false)] | sort_by(.created_at) | reverse | .[0].id // empty')" \
        || cold "could not list artifacts"
    [ -n "$id" ] || cold "none stored yet -- this run seeds one for the next"

    gh api "repos/${GITHUB_REPOSITORY}/actions/artifacts/${id}/zip" > "$tmp/artifact.zip" \
        || cold "artifact ${id} could not be downloaded"
    unzip -q "$tmp/artifact.zip" -d "$tmp/unpacked" \
        || cold "artifact ${id} is not a readable zip"

    # One archive per artifact, under whichever name and suffix pack chose.
    archive="$(find "$tmp/unpacked" -maxdepth 1 -type f -name '*.tar*' | head -1)"
    [ -n "$archive" ] || cold "artifact ${id} holds no archive"

    tar_extract "$archive" "$dest" || cold "artifact ${id} did not unpack"

    # The archive's size, not the extracted tree's: du over a restored build
    # tree is a walk of a hundred thousand files, and this runs on Windows too.
    echo "restored ${name} from artifact ${id} ($(du -h "$archive" | cut -f1) packed)"
    rm -rf "$tmp"

    # Lets a caller skip re-uploading content that cannot have changed.
    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        echo "restored=1" >> "$GITHUB_OUTPUT"
    fi
}

do_pack() {
    local archive="$1" src="$2"; shift 2
    [ -n "$archive" ] && [ -n "$src" ] || usage

    if [ ! -d "$src" ]; then
        echo "::notice::Nothing to pack: ${src} does not exist"
        exit 0
    fi

    local excludes=()
    local glob
    for glob in "$@"; do
        excludes+=("--exclude=$glob")
    done

    # One flat archive rather than the directory itself: these trees are tens of
    # thousands of small files, and upload-artifact would walk every one.
    tar_create "$archive" "${excludes[@]+"${excludes[@]}"}" -C "$src" .
    echo "packed ${archive}: $(du -h "$archive" | cut -f1)"

    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        echo "packed=1" >> "$GITHUB_OUTPUT"
    fi
}

case "${1:-}" in
    restore) shift; do_restore "${1:-}" "${2:-}" ;;
    pack)    shift; do_pack "$@" ;;
    *)       usage ;;
esac
