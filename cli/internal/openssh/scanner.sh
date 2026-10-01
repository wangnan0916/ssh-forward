# Raw facts every 2s. The next stdin line lists ports; reply with their first bytes on 127.0.0.1.
interval=2
LC_ALL=C
export LC_ALL
set -f
[ -r /proc/net/tcp ] || { echo "ssh-forward: /proc/net/tcp is unavailable" >&2; exit 1; }
uid=$(awk '$1 == "Uid:" { print $2; exit }' /proc/self/status)
case $uid in '' | *[!0-9]*) echo "ssh-forward: cannot read scanner uid" >&2; exit 1 ;; esac
has() { command -v "$1" >/dev/null 2>&1; }
has_ss=0; has ss && has_ss=1
has_meta=0; has readlink && has_meta=1
has_docker=0; has docker && [ -S /var/run/docker.sock ] && has_docker=1
has_nc=0; has nc && has_nc=1
has_probe=0; has timeout && { [ "$has_nc" -eq 1 ] || has bash; } && has_probe=1

# dd writes the line out before we wait. A pipe would otherwise stay block-buffered.
emit() { printf '%s\n' "$1" | dd bs=1048576 2>/dev/null || true; }
json_escape() { printf '%s' "$1" | awk 'BEGIN{ORS=""}{for(i=1;i<=length($0);i++){c=substr($0,i,1);if(c=="\\")printf"\\\\";else if(c=="\"")printf"\\\"";else if(c>=" ")printf"%s",c}}'; }

read_banner() {
    [ "$has_probe" -eq 1 ] || return 0
    if [ "$has_nc" -eq 1 ]; then
        timeout 0.25 nc -n -w 1 127.0.0.1 "$1" </dev/null 2>/dev/null | dd bs=8 count=1 2>/dev/null || true
    else
        timeout 0.25 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1 && dd bs=8 count=1 <&3" </dev/null 2>/dev/null || true
    fi
}

while :; do
    v6=
    [ -r /proc/sys/net/ipv6/bindv6only ] && IFS= read -r v6 < /proc/sys/net/ipv6/bindv6only

    files=/proc/net/tcp
    [ -r /proc/net/tcp6 ] && files="$files /proc/net/tcp6"
    # shellcheck disable=SC2086
    sockets=$(awk '$4 == "0A" {
        split($2, local, ":")
        printf "%s{\"addr\":\"%s\",\"port\":\"%s\",\"uid\":\"%s\",\"inode\":\"%s\"}", sep, local[1], local[2], $8, $10
        sep = ","
    }' $files 2>/dev/null || true)

    owners_json=
    processes=
    if [ "$has_ss" -eq 1 ]; then
        while read -r inode pid; do
            owners_json="${owners_json:+$owners_json,}{\"inode\":\"$inode\",\"pid\":\"$pid\"}"
            [ "$has_meta" -eq 1 ] || continue
            exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
            cwd=$(readlink "/proc/$pid/cwd" 2>/dev/null || true)
            processes="${processes:+$processes,}{\"pid\":\"$pid\",\"exe\":\"$(json_escape "$exe")\",\"cwd\":\"$(json_escape "$cwd")\"}"
        done <<EOF
$(ss -H -lntpe 2>/dev/null | awk '{
            inode = ""; pid = ""
            for (field = 1; field <= NF; field++) {
                if ($field ~ /^ino:[0-9]+$/ && inode == "") inode = substr($field, 5)
                pos = index($field, "pid=")
                if (pos > 0 && pid == "") { pid = substr($field, pos + 4); sub(/[^0-9].*$/, "", pid) }
            }
            if (inode != "" && pid != "") print inode, pid
        }' || true)
EOF
    fi

    docker_json=
    if [ "$has_docker" -eq 1 ]; then
        docker_ids=$(docker ps -q 2>/dev/null || true)
        if [ -n "$docker_ids" ]; then
            # shellcheck disable=SC2086
            while IFS=$(printf '\t') read -r port service dir name; do
                case $port in '' | *[!0-9]*) continue ;; esac
                docker_json="${docker_json:+$docker_json,}{\"port\":$port,\"service\":\"$(json_escape "$service")\",\"dir\":\"$(json_escape "$dir")\",\"name\":\"$(json_escape "$name")\"}"
            done <<EOF
$(docker inspect $docker_ids --format '{{range $p, $bindings := .NetworkSettings.Ports}}{{range $bindings}}{{if .HostPort}}{{.HostPort}}{{"\t"}}{{index $.Config.Labels "com.docker.compose.service"}}{{"\t"}}{{index $.Config.Labels "com.docker.compose.project.working_dir"}}{{"\t"}}{{$.Name}}{{"\n"}}{{end}}{{end}}{{end}}' 2>/dev/null || true)
EOF
        fi
    fi

    emit "{\"uid\":\"$uid\",\"v6\":\"$(json_escape "$v6")\",\"sockets\":[$sockets],\"owners\":[$owners_json],\"processes\":[$processes],\"docker\":[$docker_json]}"
    IFS= read -r request || exit 0
    banners=
    for port in $request; do
        case $port in '' | *[!0-9]*) continue ;; esac
        banners="${banners:+$banners,}{\"port\":$port,\"banner\":\"$(json_escape "$(read_banner "$port")")\"}"
    done
    emit "{\"banners\":[$banners]}"
    sleep "$interval"
done
