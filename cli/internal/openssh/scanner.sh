# Individual discovery failures must not end the shared SSH/SOCKS process.
# Every expansion below has a default and fallible probes degrade their output.
SSH_FORWARD_SCANNER_VERSION=1
interval=2
sequence=0
previous_fingerprint=''
listener_records=''
process_records=''
process_capability=unavailable
scans_since_attribution=30
attribution_interval=30

hex_stream() {
    od -An -v -tx1 | tr -d ' \n'
}

hex_text() {
    printf '%s' "$1" | hex_stream
}

read_process_arguments() {
    command_line=$1
    arguments_hex=$(dd if="$command_line" bs=4097 count=1 2>/dev/null | hex_stream || true)
    if [ "${#arguments_hex}" -le 8192 ]; then
        return 0
    fi
    arguments_hex=$(printf '%.8192s' "$arguments_hex")
    return 1
}

boot_id=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || printf unavailable)
network_namespace=$(readlink /proc/self/ns/net 2>/dev/null || printf unavailable)
boot_hex=$(hex_text "$boot_id")
network_hex=$(hex_text "$network_namespace")
socket_capability=full
if [ "$boot_id" = unavailable ] || [ "$network_namespace" = unavailable ]; then
    socket_capability=partial
fi
identity_socket_capability=$socket_capability
scanner_source=unavailable
base_listener_capability=unavailable
base_socket_capability=$identity_socket_capability

choose_scanner_source() {
    scanner_source=unavailable
    base_listener_capability=unavailable
    base_socket_capability=$identity_socket_capability
    if [ -r /proc/net/tcp ] && [ -r /proc/net/tcp6 ]; then
        scanner_source=proc
        base_listener_capability=full
    elif command -v ss >/dev/null 2>&1; then
        scanner_source=ss
        base_listener_capability=full
    elif command -v lsof >/dev/null 2>&1; then
        scanner_source=lsof
        base_listener_capability=partial
        base_socket_capability=partial
    elif [ -r /proc/net/tcp ] || [ -r /proc/net/tcp6 ]; then
        scanner_source=proc
        base_listener_capability=partial
    fi
}

choose_scanner_source
listener_capability=$base_listener_capability
socket_capability=$base_socket_capability

choose_fallback_source() {
    case "$scanner_source" in
        proc)
            if command -v ss >/dev/null 2>&1; then
                scanner_source=ss
                base_listener_capability=full
                base_socket_capability=$identity_socket_capability
                return
            fi
            ;;
        ss) ;;
        *)
            scanner_source=unavailable
            base_listener_capability=unavailable
            base_socket_capability=partial
            return
            ;;
    esac
    if command -v lsof >/dev/null 2>&1; then
        scanner_source=lsof
        base_listener_capability=partial
        base_socket_capability=partial
    else
        scanner_source=unavailable
        base_listener_capability=unavailable
        base_socket_capability=partial
    fi
}

scan_proc_file() {
    family=$1
    path=$2
    loopback=$3
    wildcard=$4
    [ -r "$path" ] || return 0
    awk -v family="$family" -v loopback="$loopback" -v wildcard="$wildcard" '
        function hex_digit(character) {
            character = toupper(character)
            return index("0123456789ABCDEF", character) - 1
        }
        function hex_decimal(value, result, index_) {
            result = 0
            for (index_ = 1; index_ <= length(value); index_++) {
                result = result * 16 + hex_digit(substr(value, index_, 1))
            }
            return result
        }
        NR == 1 { next }
        $4 == "0A" {
            split($2, local, ":")
            if (local[1] == loopback) {
                scope = "loopback"
            } else if (local[1] == wildcard) {
                scope = "wildcard"
            } else {
                next
            }
            port = hex_decimal(local[2])
            inode = $10
            if (port > 0 && inode ~ /^[0-9]+$/) {
                printf "%s\t%s\t%d\t%s\n", family, scope, port, inode
            }
        }
    ' "$path"
}

scan_ss() {
    ss_output=$(ss -H -ltn -e 2>/dev/null) || return 1
    printf '%s\n' "$ss_output" | awk '
        {
            local_address = $4
            inode = "0"
            for (field = 5; field <= NF; field++) {
                if ($field ~ /^ino:[0-9]+$/) {
                    inode = substr($field, 5)
                }
            }
            if (local_address ~ /^127[.]0[.]0[.]1:[0-9]+$/) {
                family = "ipv4"; scope = "loopback"
            } else if (local_address ~ /^0[.]0[.]0[.]0:[0-9]+$/ || local_address ~ /^\*:[0-9]+$/) {
                family = "ipv4"; scope = "wildcard"
            } else if (local_address ~ /^\[::1\]:[0-9]+$/) {
                family = "ipv6"; scope = "loopback"
            } else if (local_address ~ /^\[::\]:[0-9]+$/) {
                family = "ipv6"; scope = "wildcard"
            } else {
                next
            }
            port = local_address
            sub(/^.*:/, "", port)
            printf "%s\t%s\t%d\t%s\n", family, scope, port, inode
        }
    '
}

scan_lsof() {
    lsof_output=$(lsof -nP -iTCP -sTCP:LISTEN -Fn 2>/dev/null || true)
    printf '%s\n' "$lsof_output" | awk '
        /^n/ {
            local_address = substr($0, 2)
            if (local_address ~ /^127[.]0[.]0[.]1:[0-9]+$/) {
                family = "ipv4"; scope = "loopback"
            } else if (local_address ~ /^0[.]0[.]0[.]0:[0-9]+$/ || local_address ~ /^\*:[0-9]+$/) {
                family = "ipv4"; scope = "wildcard"
            } else if (local_address ~ /^\[::1\]:[0-9]+$/) {
                family = "ipv6"; scope = "loopback"
            } else if (local_address ~ /^\[::\]:[0-9]+$/) {
                family = "ipv6"; scope = "wildcard"
            } else {
                next
            }
            port = local_address
            sub(/^.*:/, "", port)
            printf "%s\t%s\t%d\t0\n", family, scope, port
        }
    '
}

scan_listeners() {
    raw_listeners=''
    case "$scanner_source" in
        proc)
            ipv4_listeners=''
            ipv6_listeners=''
            if [ -r /proc/net/tcp ]; then
                ipv4_listeners=$(scan_proc_file ipv4 /proc/net/tcp 0100007F 00000000) || return 1
            fi
            if [ -r /proc/net/tcp6 ]; then
                ipv6_listeners=$(scan_proc_file ipv6 /proc/net/tcp6 00000000000000000000000001000000 00000000000000000000000000000000) || return 1
            fi
            raw_listeners=$(printf '%s\n%s\n' "$ipv4_listeners" "$ipv6_listeners")
            ;;
        ss) raw_listeners=$(scan_ss) || return 1 ;;
        lsof) raw_listeners=$(scan_lsof) || return 1 ;;
        *) return 0 ;;
    esac
    printf '%s\n' "$raw_listeners" | LC_ALL=C sort -u | head -n 256
}

scan_current_listeners() {
    scan_status=0
    current_listeners=$(scan_listeners) || scan_status=$?
    fallback_count=0
    while [ "$scan_status" -ne 0 ] && [ "$fallback_count" -lt 3 ]; do
        previous_source=$scanner_source
        choose_fallback_source
        [ "$scanner_source" != "$previous_source" ] || break
        scan_status=0
        current_listeners=$(scan_listeners) || scan_status=$?
        fallback_count=$((fallback_count + 1))
    done
}

apply_listener_record_limit() {
    if [ "$listener_count" -lt 256 ]; then
        return
    fi
    listener_capability=partial
    socket_capability=partial
    if [ "$process_capability" != unavailable ]; then
        process_capability=partial
    fi
}

append_line() {
    current=$1
    added=$2
    if [ -z "$current" ]; then
        printf '%s' "$added"
    else
        printf '%s\n%s' "$current" "$added"
    fi
}

attribute_processes() {
    if [ "$scanner_source" = lsof ] || [ "$scanner_source" = unavailable ]; then
        process_records=''
        process_capability=unavailable
        return
    fi
    wanted=''
    for inode in $(printf '%s\n' "$listener_records" | awk -F '\t' 'NF == 4 { print $4 }' | LC_ALL=C sort -u); do
        wanted="$wanted $inode"
    done

    process_records=''
    process_record_count=0
    process_metadata_hex_size=0
    process_overflow=0
    attributed=''
    for process_path in /proc/[0-9]*; do
        [ -d "$process_path" ] || continue
        owner=${process_path#/proc/}
        matched=''
        for descriptor in "$process_path"/fd/*; do
            target=$(readlink "$descriptor" 2>/dev/null || true)
            case "$target" in
                'socket:['*']') inode=${target#socket:[}; inode=${inode%]} ;;
                *) continue ;;
            esac
            case " $wanted " in
                *" $inode "*) ;;
                *) continue ;;
            esac
            case " $matched " in
                *" $inode "*) ;;
                *) matched="$matched $inode" ;;
            esac
        done
        [ -n "$matched" ] || continue

        chain=''
        current=$owner
        depth=0
        visited=''
        while [ "$depth" -lt 16 ] && [ "$current" -gt 0 ] 2>/dev/null; do
            case " $visited " in
                *" $current "*) process_overflow=1; break ;;
            esac
            visited="$visited $current"
            status=/proc/$current/status
            [ -r "$status" ] || { process_overflow=1; break; }
            executable_hex=$(readlink -n /proc/$current/exe 2>/dev/null | hex_stream || true)
            working_directory_hex=$(readlink -n /proc/$current/cwd 2>/dev/null | hex_stream || true)
            if ! read_process_arguments /proc/$current/cmdline; then
                process_overflow=1
            fi
            chain=$(append_line "$chain" "$depth	$current	$executable_hex	$working_directory_hex	$arguments_hex")
            parent=$(awk '$1 == "PPid:" { print $2; exit }' "$status" 2>/dev/null || printf 0)
            case "$parent" in
                ''|*[!0-9]*) process_overflow=1; break ;;
            esac
            [ "$parent" -gt 1 ] || break
            current=$parent
            depth=$((depth + 1))
        done
        if [ "$depth" -ge 16 ] && [ "$current" -gt 1 ] 2>/dev/null; then
            process_overflow=1
        fi
        [ -n "$chain" ] || continue

        for inode in $matched; do
            attributed="$attributed $inode"
            while IFS= read -r chain_record; do
                [ -n "$chain_record" ] || continue
                record_hex_size=${#chain_record}
                if [ "$process_record_count" -lt 512 ] && [ $((process_metadata_hex_size + record_hex_size)) -le 262144 ]; then
                    process_records=$(append_line "$process_records" "$inode	$owner	$chain_record")
                    process_record_count=$((process_record_count + 1))
                    process_metadata_hex_size=$((process_metadata_hex_size + record_hex_size))
                else
                    process_overflow=1
                fi
            done <<EOF_CHAIN
$chain
EOF_CHAIN
        done
    done

    if [ -n "$process_records" ]; then
        process_records=$(printf '%s\n' "$process_records" | LC_ALL=C sort -t "$(printf '\t')" -k1,1n -k2,2n -k3,3n)
    fi
    process_capability=full
    if [ "$process_overflow" -ne 0 ]; then
        process_capability=partial
    fi
    for inode in $wanted; do
        case " $attributed " in
            *" $inode "*) ;;
            *) process_capability=partial; break ;;
        esac
    done
}

emit_observation() {
    sequence=$((sequence + 1))
    printf 'SF1\tB\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$sequence" "$boot_hex" "$network_hex" "$listener_capability" "$socket_capability" "$process_capability"
    while IFS= read -r listener_record; do
        [ -n "$listener_record" ] || continue
        printf 'SF1\tL\t%s\t%b\n' "$sequence" "$listener_record"
    done <<EOF_LISTENERS
$listener_records
EOF_LISTENERS
    while IFS= read -r process_record; do
        [ -n "$process_record" ] || continue
        printf 'SF1\tP\t%s\t%b\n' "$sequence" "$process_record"
    done <<EOF_PROCESSES
$process_records
EOF_PROCESSES
    printf 'SF1\tE\t%s\n' "$sequence"
}

while :; do
    scan_started=$(date +%s 2>/dev/null || printf 0)
    choose_scanner_source
    scan_current_listeners
    listener_capability=$base_listener_capability
    socket_capability=$base_socket_capability
    if [ "$scan_status" -ne 0 ]; then
        current_listeners=$listener_records
        listener_capability=partial
        socket_capability=partial
    fi
    listener_count=$(printf '%s\n' "$current_listeners" | awk 'NF { count++ } END { print count + 0 }')
    if printf '%s\n' "$current_listeners" | awk -F '\t' '$4 == "0" { found = 1 } END { exit !found }'; then
        socket_capability=partial
    fi
    fingerprint=$(printf '%s' "$current_listeners" | cksum)
    if [ "$fingerprint" != "$previous_fingerprint" ] || [ "$scans_since_attribution" -ge "$attribution_interval" ]; then
        listener_records=$current_listeners
        attribute_processes
        previous_fingerprint=$fingerprint
        scans_since_attribution=0
        if [ "$process_capability" = partial ]; then
            attribution_interval=5
        else
            attribution_interval=30
        fi
    else
        scans_since_attribution=$((scans_since_attribution + 1))
    fi
    apply_listener_record_limit
    scan_finished=$(date +%s 2>/dev/null || printf 0)
    if [ "$scan_started" -gt 0 ] 2>/dev/null && [ $((scan_finished - scan_started)) -gt "$interval" ]; then
        process_capability=partial
    fi
    emit_observation
    /bin/sleep "$interval"
done
