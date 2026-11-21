#!/bin/sh
# Starts a Debian container with sshd and its own Docker daemon, to test
# layve against a remote machine over ssh without touching ~/.ssh or
# ~/.docker. Prints the environment to use:
#
#   eval "$(test/remote/up.sh /tmp/layve-remote)"
#   layve --context debian
#
# Stop it with: docker rm -f layve-remote
set -eu
dir=${1:-/tmp/layve-remote}
port=${LAYVE_REMOTE_PORT:-2222}
here=$(cd "$(dirname "$0")" && pwd)

mkdir -p "$dir/build" "$dir/docker-config"
[ -f "$dir/key" ] || ssh-keygen -q -t ed25519 -N "" -C layve-test -f "$dir/key"
cp "$here/Dockerfile" "$here/entrypoint.sh" "$dir/build/"
cp "$dir/key.pub" "$dir/build/authorized_keys"
docker build -q -t layve-remote:test "$dir/build" >/dev/null
docker rm -f layve-remote >/dev/null 2>&1 || true
docker run -d --privileged --name layve-remote -p "127.0.0.1:$port:22" layve-remote:test >/dev/null

cat > "$dir/ssh_config" <<CFG
Host layve-debian
  HostName 127.0.0.1
  Port $port
  User root
  IdentityFile $dir/key
  IdentitiesOnly yes
  UserKnownHostsFile $dir/known_hosts
  StrictHostKeyChecking accept-new
CFG

# Wait for sshd and the inner Docker daemon.
i=0
until ssh -F "$dir/ssh_config" -o BatchMode=yes layve-debian docker info >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo "remote host did not come up" >&2; docker logs layve-remote >&2; exit 1; }
  sleep 1
done

DOCKER_CONFIG="$dir/docker-config" docker context rm -f debian >/dev/null 2>&1 || true
DOCKER_CONFIG="$dir/docker-config" docker context create debian \
  --description "Debian test host" --docker "host=ssh://root@layve-debian" >/dev/null 2>&1

echo "export DOCKER_CONFIG='$dir/docker-config'"
echo "export LAYVE_SSH_COMMAND='ssh -F $dir/ssh_config'"
