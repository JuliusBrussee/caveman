#!/bin/sh
set -eu

cachebench_data_dir=${1:-/tmp/lmcache-agentic-traces}
cachebench_revision=6e043b9e89865df3aec19fd5679286b683bfd70e
cachebench_base_url="https://huggingface.co/datasets/sammshen/lmcache-agentic-traces/resolve/${cachebench_revision}/data"

command -v curl >/dev/null 2>&1 || { echo "curl required" >&2; exit 2; }
if command -v sha256sum >/dev/null 2>&1; then
    cachebench_sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
    cachebench_sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
    echo "sha256sum or shasum required" >&2
    exit 2
fi
mkdir -p "$cachebench_data_dir"

fetch_shard() {
    cachebench_name=$1
    cachebench_expected_bytes=$2
    cachebench_expected_sha256=$3
    cachebench_target="$cachebench_data_dir/$cachebench_name"
    cachebench_partial="$cachebench_target.part"
    if [ -f "$cachebench_target" ]; then
        cachebench_actual_bytes=$(wc -c < "$cachebench_target" | tr -d ' ')
        if [ "$cachebench_actual_bytes" != "$cachebench_expected_bytes" ]; then
            echo "existing file has wrong size: $cachebench_target" >&2
            exit 1
        fi
        cachebench_actual=$(cachebench_sha256 "$cachebench_target")
        if [ "$cachebench_actual" = "$cachebench_expected_sha256" ]; then
            echo "verified $cachebench_name"
            return
        fi
        echo "existing file has wrong sha256: $cachebench_target" >&2
        exit 1
    fi
    if [ -f "$cachebench_partial" ]; then
        cachebench_partial_bytes=$(wc -c < "$cachebench_partial" | tr -d ' ')
        if [ "$cachebench_partial_bytes" -gt "$cachebench_expected_bytes" ]; then
            echo "partial file exceeds expected size: $cachebench_partial" >&2
            exit 1
        fi
    fi
    curl --fail --location --continue-at - --max-filesize "$cachebench_expected_bytes" --output "$cachebench_partial" "$cachebench_base_url/$cachebench_name"
    cachebench_actual_bytes=$(wc -c < "$cachebench_partial" | tr -d ' ')
    if [ "$cachebench_actual_bytes" != "$cachebench_expected_bytes" ]; then
        echo "size mismatch for $cachebench_name: got $cachebench_actual_bytes" >&2
        exit 1
    fi
    cachebench_actual=$(cachebench_sha256 "$cachebench_partial")
    if [ "$cachebench_actual" != "$cachebench_expected_sha256" ]; then
        echo "sha256 mismatch for $cachebench_name: got $cachebench_actual" >&2
        exit 1
    fi
    mv "$cachebench_partial" "$cachebench_target"
    echo "verified $cachebench_name"
}

fetch_shard train-00000-of-00005.parquet 502376957 7d8fde279e899c0cd97e1b4b73dd2a0d76132c533e77ac1cdd310494710d7447
fetch_shard train-00001-of-00005.parquet 555802662 594c934498c9d099e75f9d3332feaaed3587b8b0b8eeb5063e79514cc2aedc8f
fetch_shard train-00002-of-00005.parquet 476154555 476b661879f28732bf5c56af4887666194b4f489a1128af59e58bdbfdf6d1ccc
fetch_shard train-00003-of-00005.parquet 437652845 ca61176d73e4de62e273983a44531ceeacb251405f8db3b92a791d9343e2c66f
fetch_shard train-00004-of-00005.parquet 398199928 a59fe71cf84ae0beb460cabb4edfd3e04f7da945c5d6777ae5df18716eceab71
