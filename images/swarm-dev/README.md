# swarm-dev image

The runtime image plus git, Go, Node LTS, build-essential, ripgrep, patch, curl and an SSH client.
It is for agents that build and test code in a long-lived workspace. HOME, GOPATH and the tool
caches live under `/workspace`, so they survive container restarts.

Build it locally on top of a runtime image:

```sh
docker build -f images/swarm-dev/Dockerfile \
  --build-arg BASE_IMAGE=c4a-runtime:dev -t c4a-swarm-dev:dev .
```

Register it in `resource-policy.yaml` with `network: egress` and a profile sized for builds (see
`configs/resource-policy.swarm-dev.example.yaml`). Install the egress deny rules on the Docker host
with `scripts/egress-firewall.sh`.
