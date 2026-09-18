# ssh-forward supports Linux Development Hosts. Emit TCP listeners reachable
# through 127.0.0.1 every two seconds; OpenSSH carries this tiny stream.
interval=2
sequence=0
limit=256
max_app_bytes=255
max_directory_bytes=768

LC_ALL=C
export LC_ALL

if [ ! -r /proc/net/tcp ]; then
    echo "ssh-forward: /proc/net/tcp is unavailable" >&2
    exit 1
fi

has_ss=0
command -v ss >/dev/null 2>&1 && has_ss=1
has_metadata_tools=0
if command -v readlink >/dev/null 2>&1 && command -v base64 >/dev/null 2>&1 \
    && command -v cut >/dev/null 2>&1 && command -v tr >/dev/null 2>&1; then
    has_metadata_tools=1
fi
has_ssh_probe=0
if command -v timeout >/dev/null 2>&1; then
    if command -v nc >/dev/null 2>&1 || command -v bash >/dev/null 2>&1; then
        has_ssh_probe=1
    fi
fi
current_uid=$(awk '$1 == "Uid:" { print $2; exit }' /proc/self/status)
# Ports confirmed to speak SSH; retained only while they stay in the listener set.
ssh_skip_ports=''

# --- scanner helpers begin ---
# Returns 0 when the byte string is an SSH identification banner.
looks_like_ssh_banner() {
    case "$1" in
        SSH-*) return 0 ;;
    esac
    return 1
}

# Returns 0 when 127.0.0.1:port greets with an SSH banner.
is_ssh_listener() {
    port=$1
    [ "$has_ssh_probe" -eq 1 ] || return 1
    banner=''
    if command -v nc >/dev/null 2>&1; then
        # stdin must be /dev/null so nc cannot consume the caller’s port list.
        banner=$(timeout 0.25 nc -n -w 1 127.0.0.1 "$port" </dev/null 2>/dev/null | dd bs=8 count=1 2>/dev/null || true)
    else
        banner=$(timeout 0.25 bash -c "exec 3<>/dev/tcp/127.0.0.1/$port && dd bs=8 count=1 <&3" </dev/null 2>/dev/null || true)
    fi
    looks_like_ssh_banner "$banner"
}
# --- scanner helpers end ---

while :; do
    sequence=$((sequence + 1))

    # ss exposes the PID-to-socket association efficiently. It is optional:
    # port discovery remains available when ss or process details are hidden.
    socket_owners=''
    if [ "$has_ss" -eq 1 ]; then
        socket_owners=$(ss -H -lntpe 2>/dev/null | awk '
            {
                inode = ""
                pid = ""
                for (field = 1; field <= NF; field++) {
                    if ($field ~ /^ino:[0-9]+$/) {
                        inode = substr($field, 5)
                    }
                    position = index($field, "pid=")
                    if (position > 0 && pid == "") {
                        pid = substr($field, position + 4)
                        sub(/[^0-9].*$/, "", pid)
                    }
                }
                if (inode != "" && pid != "") print inode ":" pid
            }
        ')
    fi

    bindv6only=1
    if [ -r /proc/sys/net/ipv6/bindv6only ]; then
        IFS= read -r bindv6only < /proc/sys/net/ipv6/bindv6only
    fi

    listeners=$(awk -v bindv6only="$bindv6only" -v current_uid="$current_uid" '
        function hex_digit(c) {
            c = toupper(c)
            return index("0123456789ABCDEF", c) - 1
        }
        function hex_decimal(value, result, i) {
            result = 0
            for (i = 1; i <= length(value); i++) {
                result = result * 16 + hex_digit(substr(value, i, 1))
            }
            return result
        }
        NR > 1 && $4 == "0A" {
            split($2, local, ":")
            address = toupper(local[1])
            reachable = 0
            if (FILENAME == "/proc/net/tcp") {
                reachable = address == "0100007F" || \
                    (address == "00000000" && $8 == current_uid)
            } else if (bindv6only == 0) {
                reachable = (address == "00000000000000000000000000000000" && $8 == current_uid) || \
                    address == "0000000000000000FFFF00000100007F"
            }
            if (reachable) {
                port = hex_decimal(local[2])
                if (port > 0) print port, $10
            }
        }
    ' /proc/net/tcp /proc/net/tcp6 2>/dev/null | sort -k1,1n -k2,2n | \
        awk '!seen[$1]++' | head -n "$limit")

    # Drop cached SSH ports that are no longer listening.
    next_ssh_skip=''
    for cached in $ssh_skip_ports; do
        if printf '%s\n' "$listeners" | awk -v port="$cached" '$1 == port { found = 1 } END { exit !found }'; then
            next_ssh_skip="$next_ssh_skip $cached"
        fi
    done
    ssh_skip_ports=$next_ssh_skip

    printf 'PF2\tB\t%s\n' "$sequence"
    # Index owners once instead of scanning every owner for every listener.
    port_rows=$(
        {
            printf '%s\n' "$socket_owners"
            printf '\n'
            printf '%s\n' "$listeners"
        } | awk '
            NF == 0 {
                reading_listeners = 1
                next
            }
            !reading_listeners {
                split($0, owner, ":")
                if (!(owner[1] in owner_by_inode)) {
                    owner_by_inode[owner[1]] = owner[2]
                }
                next
            }
            NF >= 2 {
                print $1, owner_by_inode[$2]
            }
        '
    )
    # Keep the loop in this shell so ssh_skip_ports persists across snapshots.
    while IFS=' ' read -r port pid || [ -n "$port" ]; do
        [ -n "$port" ] || continue

        case " $ssh_skip_ports " in
            *" $port "*) continue ;;
        esac

        app=''
        directory=''
        if [ -n "$pid" ] && [ "$has_metadata_tools" -eq 1 ]; then
            executable=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
            app=$(printf '%s' "${executable##*/}" | cut -b "1-$max_app_bytes")
            directory=$(readlink "/proc/$pid/cwd" 2>/dev/null | cut -b "1-$max_directory_bytes")
        fi
        if [ "$app" = "sshd" ]; then
            ssh_skip_ports="$ssh_skip_ports $port"
            continue
        fi

        # Unknown listeners: one short banner read detects sshd without cgroup.
        if [ -z "$app" ] && is_ssh_listener "$port"; then
            ssh_skip_ports="$ssh_skip_ports $port"
            continue
        fi

        metadata='AA=='
        if [ "$has_metadata_tools" -eq 1 ]; then
            metadata=$(printf '%s\000%s' "$app" "$directory" | base64 | tr -d '\n')
            [ -n "$metadata" ] || metadata='AA=='
        fi
        printf 'PF2\tP\t%s\t%s\t%s\n' "$sequence" "$port" "$metadata"
    done <<EOF
$port_rows
EOF
    printf 'PF2\tE\t%s\n' "$sequence"
    sleep "$interval"
done
