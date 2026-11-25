#!/usr/bin/env bash
# Build the pinned RocksDB engine library for the journal storage adapter.
#
# Pins (decision D33, COMPONENT_STACK, research/ROCKSDB_STACK): RocksDB 10.4.2,
# tag v10.4.2, commit 410c5623195ecbe4699b9b5a5f622c7325cec6fe. The source
# archive is fetched by commit, never by tag name, and its SHA-256 must match
# the value below. A moved tag or a changed archive stops the build.
#
# The cmake flags are the ones the pinned Go binding (github.com/linxGnu/grocksdb
# v1.10.2) uses in its own build.sh, with two differences:
#   - the compression libraries (snappy, lz4, zlib, zstd) are not rebuilt from
#     source; the build uses the ones installed on the build machine, and
#     records their versions and digests (see "Dependency lock");
#   - FAIL_ON_WARNINGS is off. build.sh compiles lz4 1.10.0 from source; a newer
#     lz4 on the build machine (Homebrew ships 1.11.1) marks a function that
#     RocksDB 10.4.2 still calls as deprecated, which fails a warnings-as-errors
#     build. Warnings do not change the generated code.
# PORTABLE=1 is the same as in build.sh. The engine's own default is 0, which
# compiles for the processor of the build machine (-march=native). With 1 the
# compiler builds its most general code, so the library also runs on another
# x86-64 or ARM64 host. Keep it on for a library that goes into an image.
#
# Nothing is installed system-wide. The static library and the headers land in
# a local prefix (the ignored .build directory of the repository by default).
#
# Dependency lock (research/ROCKSDB_STACK requires resolved dependency locks).
# Before anything is built, the script refuses to start when one of the four
# compression headers is missing, or when the version or the library file of
# one of them cannot be found. It writes <prefix>/librocksdb.lock and prints
# the same lines at the end: for each library, the version its header declares
# and the SHA-256 of the library file the linker resolves. Where the libraries
# are shared, as on macOS with Homebrew, the test binary and any binary that
# links the adapter depend on those files at run time. The lock records what
# was used; it does not force the same files on the next build. Compare the
# lock files to see that two builds used the same libraries.
#
# The SHA-256 of the library is not the same from one build to the next: the
# engine records its compile time in one object file. Builds from one archive
# gave byte-identical object files for everything else (346 of 347). The
# SHA-256 of the source archive is the pin, not the library's.
#
# Usage:
#   build-librocksdb.sh [--prefix DIR] [--jobs N]
#
# Environment:
#   CXX                     the C++ compiler and linker driver (default: c++).
#   LIBROCKSDB_DEPS_PREFIX  optional prefix paths of locally built compression
#                           libraries, separated by semicolons. Each one must
#                           hold include/ and lib/.
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

# The compression libraries, by name and header. zlib is the one whose
# linker name differs from its name: the linker knows it as z.
readonly DEPS="snappy:snappy.h lz4:lz4.h zlib:zlib.h zstd:zstd.h"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../../../.." && pwd)"
work="$root/.build/rocksdb-${ROCKSDB_VERSION}"
prefix="$work/prefix"
jobs=""
cxx="${CXX:-c++}"

usage() {
    sed -n '/^# Usage:/,/^# On finish/p' "$0" | sed -e 's/^# \{0,1\}//' >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) prefix="$2"; shift 2 ;;
        --jobs) jobs="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "build-librocksdb: unknown argument: $1" >&2; usage; exit 2 ;;
    esac
done

case "$prefix" in
    /*) ;;
    *) echo "build-librocksdb: --prefix needs an absolute path, got: $prefix" >&2; exit 2 ;;
esac

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
if ! command -v "$cxx" >/dev/null 2>&1; then
    echo "build-librocksdb: C++ compiler not found: $cxx" >&2
    exit 1
fi

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# 0. Where the compression libraries are. Every dependency prefix adds its
#    include and library directories, for the compiler here and for the Go
#    build through the flags file.
dep_includes=()
dep_libs=()
while IFS= read -r dep; do
    [ -n "$dep" ] || continue
    case "$dep" in
        /*) ;;
        *) echo "build-librocksdb: LIBROCKSDB_DEPS_PREFIX entries must be absolute paths, got: $dep" >&2; exit 2 ;;
    esac
    dep_includes+=("-I$dep/include")
    dep_libs+=("-L$dep/lib")
done <<EOF
$(printf '%s' "${LIBROCKSDB_DEPS_PREFIX:-}" | tr ';:' '\n')
EOF
includes=(${dep_includes[@]+"${dep_includes[@]}"})
libs=(${dep_libs[@]+"${dep_libs[@]}"})

# 1. Refuse to build when a compression library is missing.
missing=""
for dep in $DEPS; do
    header="${dep#*:}"
    if ! printf '#include <%s>\nint main() { return 0; }\n' "$header" |
        "$cxx" -x c++ -fsyntax-only ${includes[@]+"${includes[@]}"} - >/dev/null 2>&1; then
        missing="$missing $header"
    fi
done
if [ -n "$missing" ]; then
    echo "build-librocksdb: compression header(s) not found:$missing" >&2
    echo "If a compression library is missing, $DEPS_HINT" >&2
    exit 1
fi

# 2. Dependency lock. One run of the preprocessor reads the four headers for
#    their versions. The library file is what the linker opens for the
#    library in a throwaway link, which is the file the adapter's link gets.
#    (The compiler's -print-file-name does not look into every directory the
#    linker searches: on macOS it finds none of the Homebrew libraries.)
macro() {
    printf '%s\n' "$macros" | awk -v key="#define $1 " 'index($0, key) == 1 { print substr($0, length(key) + 1); exit }'
}
strip_quotes() { printf '%s' "$1" | tr -d '"'; }
# Three macros that hold the parts of a version, to M.m.r. Fails when one
# of them is not declared, so that a missing macro is not written as 0.
version_of() {
    local a b c
    a="$(macro "$1")"
    b="$(macro "$2")"
    c="$(macro "$3")"
    [ -n "$a" ] && [ -n "$b" ] && [ -n "$c" ] || return 1
    printf '%d.%d.%d' "$a" "$b" "$c"
}
linker_name() {
    case "$1" in
        zlib) printf 'z' ;;
        *) printf '%s' "$1" ;;
    esac
}
resolve_library() { # the file the linker takes for the library, or nothing
    local link="$1" trace out
    out="$(mktemp)" || return 1
    trace="$(printf 'int main() { return 0; }\n' |
        "$cxx" -x c++ - ${libs[@]+"${libs[@]}"} -Wl,-t -l"$link" -o "$out" 2>/dev/null)" || { rm -f "$out"; return 1; }
    rm -f "$out"
    # The trace lists every file the linker opens, one per line. Some
    # linkers wrap each line in brackets.
    printf '%s\n' "$trace" | tr -d '[]' | awk -v lib="lib$link." '
        { for (i = 1; i <= NF; i++) { n = split($i, p, "/"); if (index(p[n], lib) == 1) { print $i; exit } } }'
}

macros="$(printf '#include <snappy.h>\n#include <lz4.h>\n#include <zlib.h>\n#include <zstd.h>\n' |
    "$cxx" -x c++ -dM -E ${includes[@]+"${includes[@]}"} - 2>/dev/null)"
mkdir -p "$prefix"
lock="$prefix/librocksdb.lock"
lock_failed=""
{
    echo "# Resolved by core/journal/storage/rocksdb/build-librocksdb.sh for RocksDB ${ROCKSDB_VERSION} commit ${ROCKSDB_COMMIT}."
    echo "# Version: what the header declares. sha256: of the library file the linker resolves."
    for dep in $DEPS; do
        name="${dep%%:*}"
        case "$name" in
            snappy) version="$(version_of SNAPPY_MAJOR SNAPPY_MINOR SNAPPY_PATCHLEVEL)" || version="" ;;
            lz4) version="$(version_of LZ4_VERSION_MAJOR LZ4_VERSION_MINOR LZ4_VERSION_RELEASE)" || version="" ;;
            zlib) version="$(strip_quotes "$(macro ZLIB_VERSION)")" ;;
            zstd) version="$(version_of ZSTD_VERSION_MAJOR ZSTD_VERSION_MINOR ZSTD_VERSION_RELEASE)" || version="" ;;
        esac
        [ -n "$version" ] || lock_failed="$lock_failed $name(version)"
        if ! file="$(resolve_library "$(linker_name "$name")")" || [ -z "$file" ]; then
            lock_failed="$lock_failed $name(library file)"
            continue
        fi
        [ -n "$version" ] || continue
        echo "$name version=$version library=$file sha256=$(sha256_of "$file")"
    done
} >"$lock"
if [ -n "$lock_failed" ]; then
    echo "build-librocksdb: the dependency lock is incomplete, so nothing is built. No version or library file found for:$lock_failed" >&2
    echo "If a compression library is missing, $DEPS_HINT" >&2
    exit 1
fi
echo "build-librocksdb: dependency lock written to $lock"

# 3. Fetch (or reuse the cached copy of) the pinned archive and verify it.
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

# 4. Extract into fresh directories. A non-empty directory is refused, because a
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

# 5. Configure and build. The compiler output goes to the file librocksdb.log
#    in the work directory, so that a failure can be read without scrolling
#    through thousands of compiler lines.
log="$work/librocksdb.log"
cmake_prefix_path="${LIBROCKSDB_DEPS_PREFIX:-}"
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
    ${cmake_prefix_path:+-DCMAKE_PREFIX_PATH="$cmake_prefix_path"} >"$log" 2>&1; then
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

# 6. Record the flags for the Go build. The binding names the libraries
#    itself; the paths say where they are. A dependency prefix that is not in
#    the linker's default search path must be on the library path here, or
#    the Go link step does not find its codec libraries.
{
    echo "# Produced by core/journal/storage/rocksdb/build-librocksdb.sh. Source it before 'go build' or 'go test'."
    echo "export CGO_CFLAGS=\"-I$prefix/include\""
    echo "export CGO_LDFLAGS=\"-L$prefix/lib${dep_libs[*]+ ${dep_libs[*]}}\""
} >"$prefix/librocksdb.env"

echo "build-librocksdb: done in $((elapsed / 60))m$((elapsed % 60))s"
echo "  library:  $lib"
echo "  sha256:   $(sha256_of "$lib")"
echo "  headers:  $prefix/include/rocksdb"
echo "  built from: RocksDB ${ROCKSDB_VERSION} ${ROCKSDB_TAG} commit ${ROCKSDB_COMMIT}, archive sha256 ${ARCHIVE_SHA256}"
echo "  dependency lock ($lock):"
grep -v '^#' "$lock" | sed 's/^/    /'
echo "To build the adapter against it:"
echo "  source $prefix/librocksdb.env"
