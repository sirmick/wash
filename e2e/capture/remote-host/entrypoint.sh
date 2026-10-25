#!/bin/sh
# Install the capture's public key for pi (passed as AUTHORIZED_KEY), keep a
# couple of the box's own services running so System Monitor has something
# besides wash to show, then run sshd in the foreground.
set -eu
install -d -m 700 -o pi -g pi /home/pi/.ssh
printf '%s\n' "$AUTHORIZED_KEY" > /home/pi/.ssh/authorized_keys
chown pi:pi /home/pi/.ssh/authorized_keys
chmod 600 /home/pi/.ssh/authorized_keys
ssh-keygen -A >/dev/null

# garage-door: polls the door sensor once a second; temp-logger: appends a
# reading a minute. Stand-ins, but real processes under pi.
su pi -c 'exec -a garage-door sh -c "while :; do sleep 1; done"' &
su pi -c 'exec -a temp-logger sh -c "while :; do date +%H:%M,21.4 >> ~/garage/readings.csv; sleep 60; done"' &

exec /usr/sbin/sshd -D -e
