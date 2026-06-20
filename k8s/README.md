# Deploying to k3s / Kubernetes

The server ships as a container image (see the root README's Docker section). To
run it on a cluster, push that image to a registry your nodes can pull, then apply
[`trmnl.yaml`](trmnl.yaml) — Namespace, ConfigMap, Deployment, and a
`LoadBalancer` Service. Works with plain `kubectl apply` or vendored into a GitOps
repo (Flux / Argo CD).

```bash
kubectl apply -f k8s/trmnl.yaml
kubectl -n trmnl rollout status deploy/trmnl
```

The device then points at `http://<node-ip>:8090` (see the device-setup section
in the root README). With k3s ServiceLB the `LoadBalancer` Service binds that port
on the node's IP; otherwise use a NodePort or your own load balancer.

## Configuration

Settings live in the `trmnl-config` ConfigMap (timezone, weather location,
refresh/render intervals, photo strategy). Edit and re-apply, then
`kubectl -n trmnl rollout restart deploy/trmnl` to pick up changes.

## Photos

The Deployment mounts a host directory at `/photos` (read-only). Drop images into
that path on the node — `scp`, or a future upload server mounting the same dir,
since hostPath is shareable across pods on a node. Prefer a PVC? Swap the volume.

```bash
scp my-photo.jpg <user>@<node>:/srv/trmnl/photos/
```

New images appear on the next render (`TRMNL_RENDER_INTERVAL`, default 5 min). The
server skips dotfiles, so macOS `._*` / `.DS_Store` junk won't break the rotation
— and `COPYFILE_DISABLE=1 tar` avoids copying them in the first place.

## Image delivery

If your registry is reachable from your workstation, the usual flow works:

```bash
docker build -t <registry>/trmnl:latest .
docker push <registry>/trmnl:latest
kubectl -n trmnl rollout restart deploy/trmnl
```

If instead you run a **ClusterIP-only in-cluster registry** (common on k3s — not
reachable from outside the cluster), `make deploy` ships the image *through a
node* you can SSH to: it imports the build into the node's containerd, then pushes
to the registry the node can reach, and rolls the deployment.

```bash
make deploy DEPLOY_HOST=<user>@<node> REGISTRY_CIP=<registry-clusterip>:5000
```

Set `DEPLOY_HOST` and `REGISTRY_CIP` once in `.env` (gitignored) to avoid repeating them.
Build on the same CPU arch as your nodes (e.g. arm64 for a Raspberry Pi) — use
`docker buildx --platform` if cross-building.

## Notes

- `/data` (devices.json) is **ephemeral** here — on pod restart the device
  re-adopts (harmless; it just gets a fresh `friendly_id`). Mount a PVC if you
  want a stable registry.
- Local non-cluster runs: `docker compose up` (see the root README).
