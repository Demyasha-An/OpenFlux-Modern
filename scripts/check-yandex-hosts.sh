#!/bin/bash
# Probe the doc URL through a specific Yandex frontend (IP + SNI + Host),
# several times, to separate flaky L4 from a vhost that does not serve the doc.
ip=$1
host=$2
path=$3
for i in 1 2 3 4 5; do
  printf 'try%s %s: ' "$i" "$ip"
  out=$(timeout 10 openssl s_client -connect "$ip:443" -servername "$host" -quiet 2>/dev/null <<EOF | head -1
GET $path HTTP/1.1
Host: $host
User-Agent: Mozilla/5.0
Connection: close

EOF
)
  if [ -n "$out" ]; then echo "$out"; else echo TIMEOUT/FAIL; fi
  sleep 1
done
