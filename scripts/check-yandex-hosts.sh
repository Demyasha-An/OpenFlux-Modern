#!/bin/bash
# Check whether reachable Yandex anycast frontends also serve disk.yandex.ru
# via SNI — lets --resolve reroute the doc host off the broken 87.250.250.0/24.
check() {
  ip=$1; host=$2
  printf '%s via %s: ' "$host" "$ip"
  subj=$(timeout 8 openssl s_client -connect "$ip:443" -servername "$host" </dev/null 2>/dev/null | openssl x509 -noout -subject 2>/dev/null)
  if [ -n "$subj" ]; then echo "TLS-OK $subj"; else echo "TIMEOUT/FAIL"; fi
}
check 213.180.204.179 disk.yandex.ru
check 77.88.21.171 disk.yandex.ru
check 87.250.250.242 passport.yandex.ru
check 87.250.250.113 ya.yandex.ru
