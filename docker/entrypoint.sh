#!/bin/sh
# Maps environment variables to openflux flags and, for the exit node,
# installs the kernel-RST-drop rule *inside this container's netns*.
#
# Why the rule: the exit node's TCP connections live in a userspace (gVisor)
# stack, so the kernel has no socket for them and answers every inbound
# SYN-ACK with an RST, tearing the tunnel down. Confining the DROP to the
# container netns is the scoped variant of upstream's host-wide rule — it
# cannot affect the host or other containers.
set -eu

role="${ROLE:-client}"
transport="${TRANSPORT:-yandex}"
listen="${SOCKS5_LISTEN:-:1080}"

case "$role" in
  client|exit-node) ;;
  *)
    echo "ROLE must be 'client' or 'exit-node' (got '$role')" >&2
    exit 2
    ;;
esac

case "$transport" in
  yandex|vyandex|oneme|cupsonline|mailru) ;;
  *)
    echo "TRANSPORT must be one of yandex, vyandex, oneme, cupsonline, mailru (got '$transport')" >&2
    exit 2
    ;;
esac

set -- "--$role" --transport "$transport"

# Exit-node mode: l4 (gVisor proxy, default) or l3 (raw SNAT/DNAT, Linux+NET_RAW).
case "${MODE:-}" in
  "") ;;
  l3|l4) set -- "$@" --mode "$MODE" ;;
  *)
    echo "MODE must be 'l3' or 'l4' (got '$MODE')" >&2
    exit 2
    ;;
esac

# App-layer codec: batched (zstd, default) or legacy (per-packet LZ4).
# MUST match the peer: an old Termux/pre-merge client speaks legacy, so a
# server paired with it must run CODEC=legacy until the client is rebuilt.
case "${CODEC:-}" in
  "") ;;
  batched|legacy) set -- "$@" --codec "$CODEC" ;;
  *)
    echo "CODEC must be 'batched' or 'legacy' (got '$CODEC')" >&2
    exit 2
    ;;
esac

case "${MOBILE:-0}" in
  1|true|yes) set -- "$@" --mobile ;;
esac

if [ -n "${MTU:-}" ]; then
  set -- "$@" --mtu "$MTU"
fi
if [ -n "${RESOLVE:-}" ]; then
  set -- "$@" --resolve "$RESOLVE"
fi

if [ "$role" = client ]; then
  set -- "$@" --socks5 "$listen"
fi

if [ -n "${URL:-}" ]; then
  set -- "$@" --url "$URL"
fi
if [ -n "${MAX_TOKEN:-}" ]; then
  set -- "$@" --maxToken "$MAX_TOKEN"
fi
if [ -n "${MAX_UID:-}" ]; then
  set -- "$@" --maxUid "$MAX_UID"
fi
if [ -n "${LOCAL_IP:-}" ]; then
  # Optional: pin the egress IP (alias IP) so the RST drop could be scoped
  # with `-s <ip>` too; inside a dedicated container netns it's usually
  # unnecessary.
  set -- "$@" --local-ip "$LOCAL_IP"
fi
case "${DEBUG:-0}" in
  1|true|yes) set -- "$@" --debug ;;
esac

if [ "$role" = exit-node ]; then
  echo "[entrypoint] dropping outbound TCP RSTs inside the container netns"
  if ! iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP; then
    echo "[entrypoint] WARNING: iptables failed (missing NET_ADMIN?); kernel RSTs will kill tunnel connections" >&2
  fi
fi

echo "[entrypoint] exec: openflux $*"
exec openflux "$@"
