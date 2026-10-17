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

If instead your registry is only reachable some other way (a ClusterIP-only
or loopback-only registry behind an SSH tunnel, say), `make deploy` builds the
image as an OCI layout and hands it to a push script of your own, then rolls the
deployment:

```bash
make deploy PUSH_IMAGE=/path/to/push-image.sh   # called as: <oci-layout-dir> trmnl:latest
```

Set `PUSH_IMAGE` once in `.env` (gitignored) to avoid repeating it. The build
uses the `ocibuilder` buildx builder (`BUILDER=`), a docker-container one, since
the default docker driver can't export OCI layouts:

```bash
docker buildx create --name ocibuilder --driver docker-container --bootstrap
```

It builds for `linux/arm64` (`PLATFORM=`) and pushes OCI rather than Docker
manifest v2, which OCI-only registries such as zot reject.

## Notes

- `/data` (devices.json) is **ephemeral** here — on pod restart the device
  re-adopts (harmless; it just gets a fresh `friendly_id`). Mount a PVC if you
  want a stable registry.
- Local non-cluster runs: `docker compose up` (see the root README).
