#!/usr/bin/env bash
# End-to-end tests for the SSH provider against a real OpenSSH server in Docker.
#
# This suite is intentionally manual: it is not referenced by any GitHub
# Actions workflow. See e2e/README.md.
set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)

keep_artifacts=${E2E_KEEP_ARTIFACTS:-0}
apt_proxy=${E2E_APT_PROXY-${HTTPS_PROXY:-${https_proxy:-}}}
image_repo=${E2E_IMAGE:-tf-ssh-e2e}

die() {
    printf 'e2e: %s\n' "$*" >&2
    exit 1
}

fail() {
    printf 'e2e: FAIL: %s\n' "$*" >&2
    exit 1
}

command -v docker >/dev/null 2>&1 || die "docker is required"
command -v go >/dev/null 2>&1 || die "go is required"
command -v ssh-keygen >/dev/null 2>&1 || die "ssh-keygen is required"
command -v ssh-keyscan >/dev/null 2>&1 || die "ssh-keyscan is required"
command -v ssh >/dev/null 2>&1 || die "ssh is required"

case "$apt_proxy" in
*$'\n'* | *'"'*) die "E2E_APT_PROXY contains an unsafe character" ;;
esac

tmp_root=${TMPDIR:-/tmp}
case "$tmp_root" in
/*) ;;
*) tmp_root="$(pwd)/$tmp_root" ;;
esac
mkdir -p -- "$tmp_root"
tmp=$(mktemp -d "$tmp_root/tf-ssh-e2e.XXXXXX") || die "cannot create a temporary directory"
session=${tmp##*.}
image="$image_repo:$session"
container="tf-ssh-e2e-$session"
container_owned=0
current_wo_token=""

cleanup() {
    rc=$?
    trap - EXIT INT TERM
    if [ "$container_owned" = 1 ]; then
        label=$(docker inspect -f '{{index .Config.Labels "tf-ssh-e2e"}}' "$container" 2>/dev/null || true)
        if [ "$label" = "1" ]; then
            docker rm -f "$container" >/dev/null 2>&1 || true
        fi
    fi
    if [ "$rc" -ne 0 ] || [ "$keep_artifacts" = 1 ]; then
        printf 'e2e: artifacts kept at %s\n' "$tmp" >&2
    else
        rm -rf -- "$tmp"
    fi
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$tmp/logs" "$tmp/work"
export TF_IN_AUTOMATION=1
export TF_INPUT=0
export CHECKPOINT_DISABLE=1
unset TF_CLI_ARGS TF_CLI_ARGS_plan TF_CLI_ARGS_apply TF_CLI_ARGS_destroy
unset TF_LOG TF_LOG_PATH TF_WORKSPACE

abs_path() {
    p=$1
    case "$p" in
    */*) ;;
    *) p="./$p" ;;
    esac
    printf '%s/%s\n' "$(CDPATH='' cd -- "$(dirname -- "$p")" && pwd)" "$(basename -- "$p")"
}

tools=()
auto_discover=1
if [ -n "${E2E_TERRAFORM+x}" ] || [ -n "${E2E_TOFU+x}" ]; then
    auto_discover=0
fi
if [ "$auto_discover" = 0 ]; then
    # Explicit selection mode: setting either variable (even to an empty value)
    # disables PATH discovery for both, so only explicitly named tools run.
    if [ -n "${E2E_TERRAFORM:-}" ]; then
        [ -x "$E2E_TERRAFORM" ] || die "E2E_TERRAFORM is not executable: $E2E_TERRAFORM"
        tools+=("terraform|$(abs_path "$E2E_TERRAFORM")")
    fi
    if [ -n "${E2E_TOFU:-}" ]; then
        [ -x "$E2E_TOFU" ] || die "E2E_TOFU is not executable: $E2E_TOFU"
        tools+=("tofu|$(abs_path "$E2E_TOFU")")
    fi
    [ "${#tools[@]}" -gt 0 ] ||
        die "E2E_TERRAFORM and/or E2E_TOFU are set but empty; unset both to auto-discover tools on PATH"
else
    # Auto-discovery mode: both variables are unset, so run every CLI found.
    if command -v terraform >/dev/null 2>&1; then
        tools+=("terraform|$(command -v terraform)")
    fi
    if command -v tofu >/dev/null 2>&1; then
        tools+=("tofu|$(command -v tofu)")
    fi
    [ "${#tools[@]}" -gt 0 ] ||
        die "no Terraform or OpenTofu CLI found on PATH; set E2E_TERRAFORM and/or E2E_TOFU"
fi

printf 'e2e: session=%s\n' "$session"
printf 'e2e: building image %s\n' "$image"
build_log="$tmp/logs/docker-build.log"
if ! docker build \
    --build-arg "APT_PROXY=$apt_proxy" \
    --tag "$image" \
    "$script_dir" >"$build_log" 2>&1; then
    cat "$build_log" >&2
    die "docker build failed"
fi
tail -n 5 "$build_log"

printf 'e2e: starting container %s\n' "$container"
docker run -d \
    --name "$container" \
    --label tf-ssh-e2e=1 \
    --publish 127.0.0.1::22 \
    "$image" >/dev/null
container_owned=1

port=$(docker port "$container" 22/tcp | sed -n '1s/.*://p')
[ -n "$port" ] || {
    docker logs "$container" >&2 || true
    die "cannot determine the random SSH port"
}
printf 'e2e: sshd on 127.0.0.1:%s\n' "$port"

ssh-keygen -q -t ed25519 -N '' -C "tf-ssh-e2e-$session" -f "$tmp/id_ed25519"
ssh-keygen -q -t ed25519 -N '' -C "tf-ssh-e2e-$session-bad" -f "$tmp/id_ed25519_bad"

install_key() {
    user=$1
    group=$2
    home=$3
    docker cp "$tmp/id_ed25519.pub" "$container:$home/.ssh/authorized_keys"
    docker exec -u root "$container" chown "$user:$group" "$home/.ssh/authorized_keys"
    docker exec -u root "$container" chmod 0700 "$home/.ssh"
    docker exec -u root "$container" chmod 0600 "$home/.ssh/authorized_keys"
}
install_key e2e e2e /home/e2e
install_key root root /root

known_hosts="$tmp/known_hosts"
: >"$known_hosts"
for _ in $(seq 1 60); do
    if ssh-keyscan -p "$port" -T 2 127.0.0.1 >"$tmp/keyscan" 2>/dev/null && [ -s "$tmp/keyscan" ]; then
        cat "$tmp/keyscan" >"$known_hosts"
        break
    fi
    sleep 0.5
done
[ -s "$known_hosts" ] || {
    docker logs "$container" >&2 || true
    die "sshd did not become reachable"
}
chmod 0644 "$known_hosts"

ssh_opts=(
    -i "$tmp/id_ed25519"
    -o BatchMode=yes
    -o IdentitiesOnly=yes
    -o StrictHostKeyChecking=yes
    -o "UserKnownHostsFile=$known_hosts"
    -o ConnectTimeout=5
    -p "$port"
)

wait_ssh() {
    user=$1
    for _ in $(seq 1 30); do
        if ssh "${ssh_opts[@]}" "$user@127.0.0.1" true >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}
wait_ssh e2e || {
    docker logs "$container" >&2 || true
    die "public key login as e2e failed"
}
wait_ssh root || {
    docker logs "$container" >&2 || true
    die "public key login as root failed"
}
printf 'e2e: public key authentication verified for e2e and root\n'

provider_dir="$tmp/provider"
mkdir -p "$provider_dir"
(
    cd "$repo_root"
    go build -mod=readonly -buildvcs=false -trimpath -o "$provider_dir/terraform-provider-ssh" .
)
printf 'e2e: provider built from the working tree\n'

tf_cli_config="$tmp/terraform.tfrc"
printf 'provider_installation {\n  dev_overrides {\n    "geektr-cloud/ssh" = "%s"\n  }\n  direct {}\n}\n' \
    "$provider_dir" >"$tf_cli_config"
export TF_CLI_CONFIG_FILE="$tf_cli_config"

invoke() {
    dir=$1
    log=$2
    shift 2
    printf 'e2e: $ %s\n' "$*"
    set +e
    (
        cd "$dir" && "$@"
    ) >"$log" 2>&1
    rc=$?
    set -e
    cat "$log"
    return "$rc"
}

log_contains() {
    log=$1
    match=$2
    # Strip ANSI colors and the diagnostic box-drawing gutter, then flatten
    # wrapped lines to spaces so a phrase split across lines still matches.
    sed $'s/\x1b\\[[0-9;]*m//g' "$log" |
        LC_ALL=C tr -cd '[:print:]\n\r\t' |
        tr '\n\r\t' '   ' |
        tr -s ' ' |
        grep -F -- "$match" >/dev/null
}

expect_fail() {
    dir=$1
    log=$2
    match=$3
    shift 3
    if invoke "$dir" "$log" "$@"; then
        fail "command unexpectedly succeeded: $*"
    fi
    if ! log_contains "$log" "$match"; then
        fail "expected failure text not found in $log: $match"
    fi
}

remote() {
    docker exec -u root "$container" "$@"
}

remote_install_dir() {
    path_to_make=$1
    mode=$2
    owner=$3
    remote install -d -m "$mode" -o "${owner%%:*}" -g "${owner##*:}" -- "$path_to_make"
}

create_remote_file() {
    path_to_write=$1
    content=$2
    mode=$3
    owner=$4
    printf '%s' "$content" | docker exec -i -u root "$container" sh -ec '
        umask 077
        cat > "$1"
        chmod "$2" -- "$1"
        chown "$3" -- "$1"
    ' sh "$path_to_write" "$mode" "$owner"
}

assert_remote_content() {
    p=$1
    want=$2
    if ! got=$(remote cat -- "$p" 2>/dev/null); then
        fail "cannot read remote path $p"
    fi
    [ "$got" = "$want" ] || fail "content mismatch at $p"
}

assert_remote_stat() {
    p=$1
    want=$2
    if ! got=$(remote stat -c '%a:%U:%G' -- "$p" 2>/dev/null); then
        fail "cannot stat remote path $p"
    fi
    [ "$got" = "$want" ] || fail "stat mismatch at $p: got $got, want $want"
}

assert_remote_missing() {
    p=$1
    if remote test -e "$p"; then
        fail "remote path still exists: $p"
    fi
}

append_conn_tfvars() {
    f=$1
    key_file=${2:-$tmp/id_ed25519}
    {
        printf 'host = "127.0.0.1"\n'
        printf 'port = "%s"\n' "$port"
        printf 'ssh_user = "e2e"\n'
        printf 'known_hosts = "%s"\n' "$known_hosts"
        printf 'private_key = <<EOT\n'
        cat "$key_file"
        printf 'EOT\n'
    } >>"$f"
}

write_basic_tfvars() {
    f=$1
    root_base=$2
    public_base=$3
    seed_file=$4
    secret_file=$5
    run_token=$6
    : >"$f"
    append_conn_tfvars "$f"
    {
        printf 'root_base = "%s"\n' "$root_base"
        printf 'public_base = "%s"\n' "$public_base"
        printf 'seed_file = "%s"\n' "$seed_file"
        printf 'secret_file = "%s"\n' "$secret_file"
        printf 'run_token = "%s"\n' "$run_token"
    } >>"$f"
    chmod 0600 "$f"
}

write_negative_tfvars() {
    f=$1
    scenario_path=$2
    : >"$f"
    append_conn_tfvars "$f"
    printf 'path = "%s"\n' "$scenario_path" >>"$f"
    chmod 0600 "$f"
}

assert_output() {
    dir=$1
    key=$2
    want=$3
    if ! got=$(cd "$dir" && "$current_bin" output -raw "$key" 2>/dev/null); then
        fail "terraform output $key failed"
    fi
    [ "$got" = "$want" ] || fail "output $key mismatch"
}

# Compare exact bytes, including trailing newlines that command substitution
# would strip.
assert_output_exact() {
    dir=$1
    key=$2
    want=$3
    got="$tmp/output-$key.raw"
    expected="$tmp/output-$key.want"
    if ! (cd "$dir" && "$current_bin" output -raw "$key") >"$got" 2>/dev/null; then
        fail "terraform output $key failed"
    fi
    printf '%s' "$want" >"$expected"
    if ! cmp -s "$got" "$expected"; then
        fail "output $key exact mismatch"
    fi
}

plan_expect() {
    expected=$1
    log=$2
    plan_file=$3
    dir=$4
    var_file=$5
    set +e
    (
        cd "$dir" && "$current_bin" plan -detailed-exitcode -input=false -var-file="$var_file" -out="$plan_file"
    ) >"$log" 2>&1
    rc=$?
    set -e
    cat "$log"
    [ "$rc" -eq "$expected" ] || fail "plan exit code $rc, want $expected (see $log)"
}

scan_plan_for_writeonly() {
    dir=$1
    plan_file=$2
    secret=$3
    json=$4
    (
        cd "$dir" && "$current_bin" show -json "$plan_file"
    ) >"$json" || fail "cannot expand plan $plan_file"
    if grep -F -q -- "$secret" "$json"; then
        fail "plan $plan_file contains write-only plaintext"
    fi
}

scan_state_for_writeonly() {
    dir=$1
    secret=$2
    for f in "$dir/terraform.tfstate" "$dir/terraform.tfstate.backup"; do
        [ -f "$f" ] || continue
        if grep -F -q -- "$secret" "$f"; then
            fail "state $f contains write-only plaintext"
        fi
    done
}

scan_logs() {
    dir=$1
    secret=$2
    if grep -R -F -q -- 'BEGIN OPENSSH PRIVATE KEY' "$dir"; then
        fail "a log contains private key material"
    fi
    if grep -R -F -q -- "$secret" "$dir"; then
        fail "a log contains write-only plaintext"
    fi
}

run_basic() {
    tool_name=$1
    logs=$current_logs/basic
    mkdir -p "$logs"
    work="$current_work/basic"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/basic/." "$work/"

    root_base="/root/tf-ssh-e2e-$session-$tool_name"
    public_base="/tmp/tf-ssh-e2e-$session-$tool_name"
    seed_token="seed-$session-$tool_name-$RANDOM$RANDOM"
    current_wo_token="wo-$session-$tool_name-$RANDOM$RANDOM"
    run_token="run-$session-$tool_name-$RANDOM$RANDOM"
    seed_file="$root_base/seed.txt"
    secret_file="$root_base/secret.txt"

    remote_install_dir "$root_base" 0755 root:root
    create_remote_file "$seed_file" "$seed_token" 0644 root:root
    secret_source=$(printf '  %s  \n' "$current_wo_token")
    create_remote_file "$secret_file" "$secret_source" 0600 root:root

    var_file="$work/e2e.tfvars"
    write_basic_tfvars "$var_file" "$root_base" "$public_base" "$seed_file" "$secret_file" "$run_token"

    printf 'e2e: basic fixture\n'
    invoke "$work" "$logs/apply.log" "$current_bin" apply -auto-approve -input=false -var-file="$var_file"

    assert_remote_stat "$root_base/dir" "750:root:root"
    assert_remote_content "$root_base/dir/plain.txt" "plain-content"
    assert_remote_stat "$root_base/dir/plain.txt" "640:root:root"
    assert_remote_content "$root_base/dir/secret.txt" "$current_wo_token"
    assert_remote_stat "$root_base/dir/secret.txt" "600:root:root"
    assert_remote_stat "$public_base/dir" "755:e2e:e2e"
    assert_remote_content "$public_base/dir/hello.txt" "hello-world"
    assert_remote_stat "$public_base/dir/hello.txt" "644:e2e:e2e"

    # The e2e user cannot read the /root path but sudo -n works.
    if ssh "${ssh_opts[@]}" e2e@127.0.0.1 "cat -- '$root_base/dir/plain.txt'" >/dev/null 2>&1; then
        fail "e2e user could read the /root path without sudo"
    fi
    if ! sudo_uid=$(ssh "${ssh_opts[@]}" e2e@127.0.0.1 'sudo -n id -u' 2>/dev/null); then
        fail "sudo -n is not available to the e2e user"
    fi
    [ "$sudo_uid" = "0" ] || fail "sudo -n did not run as root"

    digest=$(printf '%s' "$current_wo_token" | sha256sum | cut -d' ' -f1)
    assert_output "$work" seed_value "$seed_token"
    assert_output "$work" uname_value "Linux"
    assert_output "$work" run_value "$run_token"
    assert_output "$work" padded_value "padded"
    assert_output_exact "$work" keep_newline_value $'keep-newline\n'
    assert_output "$work" sensitive_seed_value "$seed_token"
    assert_output "$work" sensitive_padded_value "secret-padded"
    assert_output "$work" wo_digest "$digest"

    scan_state_for_writeonly "$work" "$current_wo_token"
    plan_expect 0 "$logs/plan-stable.log" "$work/plan-stable.bin" "$work" "$var_file"
    scan_plan_for_writeonly "$work" "$work/plan-stable.bin" "$current_wo_token" "$work/plan-stable.json"
    scan_state_for_writeonly "$work" "$current_wo_token"

    # Damage both a plain file and the write-only file, then let apply repair.
    create_remote_file "$root_base/dir/plain.txt" "drifted-content" 0644 root:root
    create_remote_file "$root_base/dir/secret.txt" "drifted-write-only" 0644 root:root
    plan_expect 2 "$logs/plan-drift.log" "$work/plan-drift.bin" "$work" "$var_file"
    scan_plan_for_writeonly "$work" "$work/plan-drift.bin" "$current_wo_token" "$work/plan-drift.json"
    invoke "$work" "$logs/repair.log" "$current_bin" apply -auto-approve -input=false -var-file="$var_file"

    assert_remote_content "$root_base/dir/plain.txt" "plain-content"
    assert_remote_stat "$root_base/dir/plain.txt" "640:root:root"
    assert_remote_content "$root_base/dir/secret.txt" "$current_wo_token"
    assert_remote_stat "$root_base/dir/secret.txt" "600:root:root"
    scan_state_for_writeonly "$work" "$current_wo_token"

    invoke "$work" "$logs/destroy.log" "$current_bin" destroy -auto-approve -input=false -var-file="$var_file"
    assert_remote_missing "$root_base/dir/plain.txt"
    assert_remote_missing "$root_base/dir/secret.txt"
    assert_remote_missing "$root_base/dir"
    assert_remote_missing "$public_base/dir/hello.txt"
    assert_remote_missing "$public_base/dir"
}

run_negative_existing() {
    logs=$current_logs/negative-existing
    mkdir -p "$logs"
    work="$current_work/negative-existing"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/negative-existing/." "$work/"
    base="/tmp/tf-ssh-e2e-$session-$current_tool-negative-existing"
    target="$base/target.txt"
    remote_install_dir "$base" 0755 root:root
    create_remote_file "$target" "original-content" 0644 root:root
    var_file="$work/e2e.tfvars"
    write_negative_tfvars "$var_file" "$target"

    printf 'e2e: negative fixture: existing path rejected\n'
    expect_fail "$work" "$logs/apply.log" "already exists; set auto_import = true" \
        "$current_bin" apply -auto-approve -input=false -var-file="$var_file"
    assert_remote_content "$target" "original-content"
    assert_remote_stat "$target" "644:root:root"
}

run_negative_autoimport() {
    logs=$current_logs/negative-autoimport
    mkdir -p "$logs"
    work="$current_work/negative-autoimport"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/negative-autoimport/." "$work/"
    base="/tmp/tf-ssh-e2e-$session-$current_tool-negative-autoimport"
    target="$base/target.txt"
    remote_install_dir "$base" 0755 root:root
    create_remote_file "$target" "original-content" 0644 root:root
    var_file="$work/e2e.tfvars"
    write_negative_tfvars "$var_file" "$target"

    printf 'e2e: negative fixture: auto_import adoption\n'
    invoke "$work" "$logs/apply.log" "$current_bin" apply -auto-approve -input=false -var-file="$var_file"
    assert_remote_content "$target" "adopted-content"
    assert_remote_stat "$target" "640:root:root"
    plan_expect 0 "$logs/plan-stable.log" "$work/plan-stable.bin" "$work" "$var_file"
    invoke "$work" "$logs/destroy.log" "$current_bin" destroy -auto-approve -input=false -var-file="$var_file"
    assert_remote_missing "$target"
}

run_negative_nonempty() {
    logs=$current_logs/negative-nonempty
    mkdir -p "$logs"
    work="$current_work/negative-nonempty"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/negative-nonempty/." "$work/"
    base="/tmp/tf-ssh-e2e-$session-$current_tool-negative-nonempty"
    target="$base/dir"
    child="$target/unmanaged-child.txt"
    remote_install_dir "$target" 0755 root:root
    create_remote_file "$child" "unmanaged-child-content" 0644 root:root
    var_file="$work/e2e.tfvars"
    write_negative_tfvars "$var_file" "$target"

    printf 'e2e: negative fixture: non-empty directory destroy\n'
    invoke "$work" "$logs/apply.log" "$current_bin" apply -auto-approve -input=false -var-file="$var_file"
    assert_remote_stat "$target" "755:root:root"
    expect_fail "$work" "$logs/destroy.log" "must be empty" \
        "$current_bin" destroy -auto-approve -input=false -var-file="$var_file"
    assert_remote_content "$child" "unmanaged-child-content"
    assert_remote_stat "$target" "755:root:root"
}

run_negative_auth() {
    logs=$current_logs/negative-auth
    mkdir -p "$logs"
    work="$current_work/negative-auth"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/negative-auth/." "$work/"
    base="/tmp/tf-ssh-e2e-$session-$current_tool-negative-auth"
    target="$base/should-not-exist.txt"
    remote_install_dir "$base" 0755 root:root
    var_file="$work/e2e.tfvars"
    : >"$var_file"
    append_conn_tfvars "$var_file" "$tmp/id_ed25519_bad"
    printf 'path = "%s"\n' "$target" >>"$var_file"
    chmod 0600 "$var_file"

    printf 'e2e: negative fixture: authentication failure\n'
    expect_fail "$work" "$logs/apply.log" "unable to authenticate" \
        "$current_bin" apply -auto-approve -input=false -var-file="$var_file"
    assert_remote_missing "$target"
}

run_negative_owner() {
    logs=$current_logs/negative-owner
    mkdir -p "$logs"
    work="$current_work/negative-owner"
    mkdir -p "$work"
    cp -R "$script_dir/fixtures/negative-owner/." "$work/"
    base="/tmp/tf-ssh-e2e-$session-$current_tool-negative-owner"
    target="$base/target.txt"
    remote_install_dir "$base" 0755 root:root
    create_remote_file "$target" "keep-me" 0640 root:root
    var_file="$work/e2e.tfvars"
    write_negative_tfvars "$var_file" "$target"
    printf 'bad_owner = "no-such-user-%s"\n' "$session" >>"$var_file"

    printf 'e2e: negative fixture: invalid owner atomicity\n'
    expect_fail "$work" "$logs/apply.log" "resolve user" \
        "$current_bin" apply -auto-approve -input=false -var-file="$var_file"
    assert_remote_content "$target" "keep-me"
    assert_remote_stat "$target" "640:root:root"
}

run_tool() {
    current_tool=$1
    current_bin=$2
    current_logs="$tmp/logs/$current_tool"
    current_work="$tmp/work/$current_tool"
    mkdir -p "$current_logs" "$current_work"

    version=$("$current_bin" version | head -n 1)
    printf '\ne2e: ===== %s (%s) =====\n' "$current_tool" "$version"

    run_basic "$current_tool"
    run_negative_existing
    run_negative_autoimport
    run_negative_nonempty
    run_negative_auth
    run_negative_owner

    scan_logs "$current_logs" "$current_wo_token"
    printf 'e2e: PASS %s (%s)\n' "$current_tool" "$version"
    report+=("PASS $version")
}

report=()
for entry in "${tools[@]}"; do
    run_tool "${entry%%|*}" "${entry#*|}"
done

printf '\ne2e: all checks passed\n'
for line in "${report[@]}"; do
    printf '  %s\n' "$line"
done
