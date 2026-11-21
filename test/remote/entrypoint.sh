#!/bin/sh
dockerd --storage-driver=vfs >/var/log/dockerd.log 2>&1 &
exec /usr/sbin/sshd -D -e
