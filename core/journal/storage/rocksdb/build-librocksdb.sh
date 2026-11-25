#!/usr/bin/env bash
# Build the pinned RocksDB engine library for the journal storage adapter.
#
# Pins (decision D33, COMPONENT_STACK, research/ROCKSDB_STACK): RocksDB 10.4.2,
# tag v10.4.2, commit 410c5623195ecbe4699b9b5a5f622c7325cec6fe. The source
# archive is fetched by commit, never by tag name, and its SHA-256 must match
# the value below. A moved tag or a changed archive stops the build.
#
# The cmake flags are the ones the pinned Go binding (github.com/linxGnu/grocksdb
# v1.10.2) uses in its own build.sh, with three differences:
#   - the compression libraries (snappy, lz4, zlib, zstd) are not rebuilt from
#     source; the build uses the ones installed on the build machine;
#   - PORTABLE=1 stays on, as in that script. The engine's own default is 0,
#     which compiles for the processor of the build machine (-march=native).
#     With 1 the compiler builds its most general code, so the library also
#     runs on another x86-64 or ARM64 host. Keep it on for a library that goes
#     into an image.
#   - FAIL_ON_WARNINGS is off. build.sh compiles lz4 1.10.0 from source; a newer
#     lz4 on the build machine (Homebrew ships 1.11.1) marks a function that
#     RocksDB 10.4.2 still calls as deprecated, which fails a warnings-as-errors
#     build. Warnings do not change the generated code.
#
# Nothing is installed system-wide. The static library and the headers land in
# a local prefix (the ignored .build directory of the repository by default).
#
# The SHA-256 of the library is not the same from one build to the next: the
# engine records its compile time in one object file. Two builds from one
# archive gave byte-identical object files for everything else (346 of 347).
# The SHA-256 of the source archive is the pin, not the library's.
#
# Usage:
#   build-librocksdb.sh [--prefix DIR] [--jobs N]
#
# Environment:
#   LIBROCKSDB_DEPS_PREFIX  optional semicolon-separated cmake prefix paths for
#                           locally built compression libraries.
#
# On finish the script prints the CGO flags to use when building the adapter
# and writes the same flags to <prefix>/librocksdb.env for "source".

set -euo pipefail

readonly ROCKSDB_VERSION="10.4.2"
readonly ROCKSDB_TAG="v10.4.2"
readonly ROCKSDB_COMMIT="410c5623195ecbe4699b9b5a5f622c7325cec6fe"
readonly ARCHIVE_SHA256="41d782ccb972a032425a5e5558dd44016efebac38bdeeb143a13a87d35e3a87d"
readonly ARCHIVE_URL="https://github.com/facebook/rocksdb/archive/${ROCKSDB_COMMIT}.tar.gz"

readonly DEPS_HINT="install them first, for example: brew install snappy lz4 zstd (macOS) or
       apt-get install libsnappy-dev zlib1g-dev liblz4-dev libzstd-dev (Debian or Ubuntu),
       or build them into a prefix and set LIBROCKSDB_DEPS_PREFIX."

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../../../.." && pwd)"
work="$root/.build/rocksdb-${ROCKSDB_VERSION}"
prefix="$work/prefix"
jobs=""

usage() {
    sed -n '/^# Usage:/,/^# Environment:/p' "$0" | sed -e 's/^# \{0,1\}//' >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) prefix="$2"; shift 2 ;;
        --jobs) jobs="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "build-librocksdb: unknown argument: $1" >&2; usage; exit 2 ;;
    esac
done

if [ -z "$jobs" ]; then
    cpus="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
    jobs=$((cpus / 2))
    [ "$jobs" -ge 1 ] || jobs=1
fi

for tool in cmake curl tar make; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "build-librocksdb: required tool is missing: $tool" >&2
        exit 1
    fi
done

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# 1. Fetch (or reuse the cached copy of) the pinned archive and verify it.
archive="$work/dist/${ROCKSDB_COMMIT}.tar.gz"
mkdir -p "$work/dist"
if [ ! -f "$archive" ]; then
    echo "build-librocksdb: downloading RocksDB ${ROCKSDB_VERSION} (${ROCKSDB_COMMIT})"
    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        --output "$archive.part" "$ARCHIVE_URL"
    mv "$archive.part" "$archive"
fi
actual="$(sha256_of "$archive")"
if [ "$actual" != "$ARCHIVE_SHA256" ]; then
    echo "build-librocksdb: SHA-256 mismatch for $archive" >&2
    echo "  want $ARCHIVE_SHA256" >&2
    echo "  got  $actual" >&2
    echo "The cached file is not the pinned source. Remove it and check the network before trying again." >&2
    exit 1
fi

# 2. Extract into fresh directories. A non-empty directory is refused, because a
#    stale cmake cache or a half-extracted tree would be built by mistake.
src="$work/src"
build="$work/build"
for dir in "$src" "$build"; do
    if [ -e "$dir" ] && [ -n "$(ls -A "$dir")" ]; then
        echo "build-librocksdb: $dir is not empty. Remove it (rm -rf \"$dir\") and run again." >&2
        exit 1
    fi
done
mkdir -p "$src" "$build"
tar -xzf "$archive" -C "$src" --strip-components=1

# 3. Configure and build. The compiler output goes to the file librocksdb.log
#    in the work directory, so that a failure can be read without scrolling
#    through thousands of compiler lines.
log="$work/librocksdb.log"
deps="${LIBROCKSDB_DEPS_PREFIX:-}"
echo "build-librocksdb: configuring (log: $log)"
if ! cmake -S "$src" -B "$build" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
    -DCMAKE_INSTALL_PREFIX="$prefix" \
    -DWITH_TESTS=OFF \
    -DWITH_GFLAGS=OFF \
    -DWITH_BENCHMARK_TOOLS=OFF \
    -DWITH_TOOLS=OFF \
    -DWITH_MD_LIBRARY=OFF \
    -DWITH_RUNTIME_DEBUG=OFF \
    -DROCKSDB_BUILD_SHARED=OFF \
    -DWITH_SNAPPY=ON \
    -DWITH_LZ4=ON \
    -DWITH_ZLIB=ON \
    -DWITH_LIBURING=OFF \
    -DWITH_ZSTD=ON \
    -DWITH_BZ2=OFF \
    -DPORTABLE=1 \
    -DFAIL_ON_WARNINGS=OFF \
    ${deps:+-DCMAKE_PREFIX_PATH="$deps"} >"$log" 2>&1; then
    echo "build-librocksdb: cmake failed. Last lines of $log:" >&2
    tail -n 25 "$log" >&2
    echo "If a compression library was not found, $DEPS_HINT" >&2
    exit 1
fi

echo "build-librocksdb: building with $jobs jobs (log: $log)"
start="$(date +%s)"
if ! cmake --build "$build" --target install/strip -j "$jobs" >>"$log" 2>&1; then
    echo "build-librocksdb: build failed. Errors from $log:" >&2
    grep -n -E 'error:|Error |\*\*\*' "$log" | tail -n 40 >&2 || true
    exit 1
fi
elapsed=$(( $(date +%s) - start ))

lib="$prefix/lib/librocksdb.a"
if [ ! -f "$lib" ] || [ ! -f "$prefix/include/rocksdb/version.h" ]; then
    echo "build-librocksdb: the build reported success but $lib or the headers are missing" >&2
    exit 1
fi

# 4. Record the flags for the Go build. Only the paths are needed: the pinned
#    binding names the libraries itself in its cgo directives.
{
    echo "# Produced by core/journal/storage/rocksdb/build-librocksdb.sh. Source it before 'go build' or 'go test'."
    echo "export CGO_CFLAGS=\"-I$prefix/include\""
    echo "export CGO_LDFLAGS=\"-L$prefix/lib\""
} >"$prefix/librocksdb.env"

echo "build-librocksdb: done in $((elapsed / 60))m$((elapsed % 60))s"
echo "  library:  $lib"
echo "  sha256:   $(sha256_of "$lib")"
echo "  headers:  $prefix/include/rocksdb"
echo "  built from: RocksDB ${ROCKSDB_VERSION} ${ROCKSDB_TAG} commit ${ROCKSDB_COMMIT}, archive sha256 ${ARCHIVE_SHA256}"
echo "To build the adapter against it:"
echo "  source $prefix/librocksdb.env"
