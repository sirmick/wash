# wash: auto-discover the running wash-router's control socket
# so `wash-launch <app-id>` works without arg-fiddling for any
# shell that sources /etc/profile (busybox getty + ash do).
#
# wash-router binds /tmp/wash-${uid}.sock (router.go:166). The
# in-VM router runs as user `wash` (uid 100); a getty login as
# root would otherwise look for /tmp/wash-0.sock and fail with
# "wash-launch: no socket". Globbing the first matching socket
# keeps this future-proof if the router ever runs as a different
# user.

for _wash_sock in /tmp/wash-*.sock; do
    if [ -S "$_wash_sock" ]; then
        export WASH_CONTROL_SOCKET="$_wash_sock"
        break
    fi
done
unset _wash_sock
