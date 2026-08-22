# ssh-forward supports Linux Development Hosts. Emit IPv4 loopback TCP
# listeners from procfs every two seconds; OpenSSH carries this tiny stream.
interval=2
sequence=0
limit=256

if [ ! -r /proc/net/tcp ]; then
    echo "ssh-forward: /proc/net/tcp is unavailable" >&2
    exit 1
fi

while :; do
    sequence=$((sequence + 1))
    printf 'PF1\tB\t%s\n' "$sequence"
    awk '
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
            if (local[1] == "0100007F") {
                port = hex_decimal(local[2])
                if (port > 0) print port
            }
        }
    ' /proc/net/tcp | LC_ALL=C sort -nu | head -n "$limit" | while IFS= read -r port; do
        printf 'PF1\tP\t%s\t%s\n' "$sequence" "$port"
    done
    printf 'PF1\tE\t%s\n' "$sequence"
    sleep "$interval"
done
