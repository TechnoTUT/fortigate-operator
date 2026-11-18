# fortigate-operator

A Kubernetes controller that manages FortiGate firewall policies, address objects and custom services from manifests. It writes them through the FortiOS REST API, so they can be kept in Git next to the applications that need them and applied with Argo CD or `kubectl`.

TechnoTUT uses it with the cluster defined in [TechnoTUT/Infra](https://github.com/TechnoTUT/Infra), against a FortiGate-100D running FortiOS 6.2.

## Resources

All kinds are namespaced and belong to `fortigate.technotut.net/v1alpha1`. Field names and values follow the FortiOS CLI (`set srcintf ...`, `set nat enable`).

| Kind | FortiGate table | Short name |
|---|---|---|
| `FirewallPolicy` | `config firewall policy` | `fgpol` |
| `FirewallAddress` | `config firewall address` (ipmask, iprange, fqdn) | `fgaddr` |
| `FirewallService` | `config firewall service custom` (TCP, UDP, SCTP) | `fgsvc` |

```yaml
apiVersion: fortigate.technotut.net/v1alpha1
kind: FirewallAddress
metadata:
  name: cam1-rtmp
  namespace: cam1
spec:
  subnet: 10.12.1.10/32
---
apiVersion: fortigate.technotut.net/v1alpha1
kind: FirewallPolicy
metadata:
  name: al-cam1-rtmp
  namespace: cam1
spec:
  srcintf: [VLAN44]
  dstintf: [dmz]
  srcaddr: [VLAN44 address]
  dstaddr: [cam1-rtmp]
  service: [RTMP]
  action: accept      # default accept
  nat: disable        # default disable
  status: enable      # default enable
  logtraffic: utm     # default utm
  schedule: always    # default always
  comments: cam1 to its RTMP server
  position:
    before: deny-cam1 # optional
```

A policy refers to interfaces, addresses and services by their FortiGate names. Objects created by this controller and objects created by hand can be mixed. More examples are in [config/samples](config/samples).

```console
$ kubectl get fortigate -A
NAMESPACE   NAME           FORTIGATE NAME   ID   ACTION   READY   REASON   AGE
cam1        al-cam1-rtmp   al-cam1-rtmp     28   accept   True    Synced   3m
```

### Common fields

- `spec.name`: name on the FortiGate. Defaults to `metadata.name`. It cannot be changed later. Policy names are limited to 35 characters and address and service names to 79.
- `spec.deletionPolicy`: `Delete` (default) removes the FortiGate object when the resource is deleted. `Orphan` leaves it.
- `status.conditions[type=Ready]`: `True` with reason `Synced` when the FortiGate matches the spec. Other reasons are `Conflict`, `FortiGateError`, `InvalidSpec`, `ReferenceNotFound`, `NotConverged`, `DeleteFailed` and `DryRun`. The message gives the details, including the FortiGate error for a rejected write (for example a policy that refers to an address that does not exist).

## How it works

- Each resource is compared with the FortiGate object field by field. Only the fields that differ are written, and nothing is written when they match, so the FortiGate does not save its configuration (and add a config revision) on every check.
- Every resource is checked again every `RESYNC_INTERVAL` (5 minutes by default). Changes made on the FortiGate by hand to a managed object are reverted.
- Reference lists (`srcaddr`, `service` and so on) are compared as sets. Their order does not matter.
- After a write the object is read back. If the FortiGate stored something different from what was written, the resource gets `NotConverged` with the field names instead of being rewritten silently forever.
- A finalizer removes the FortiGate object before the resource goes away. When a deletion is refused (for example an address still used by a policy that is not managed here), the resource stays with `DeleteFailed` and is retried.
- Policies are reconciled one at a time. The full policy list is read on each reconcile to find the policy by name and to see the evaluation order.

### Ownership

The controller marks every object it creates by putting `[k8s:<namespace>/<name>]` at the start of the comment field (`comments` for policies, `comment` for addresses and services). Your own comment follows the marker. The marker is on the FortiGate itself, so it survives a rebuild of the cluster: the same manifests take their objects back.

- An existing object with the same name and no marker is never changed or deleted. The resource reports `Conflict`.
- To bring an existing object under management, add the annotation `fortigate.technotut.net/adopt: "true"`. The controller then overwrites it with the spec and adds the marker.
- An object marked by another resource is never taken over, even with the annotation. This catches two namespaces that pick the same FortiGate name.

### Policy order

FortiGate evaluates policies from the top. A new policy is added at the end of the list (above the implicit deny). To keep it above or below another policy, set `spec.position.before` or `spec.position.after` to the other policy's FortiGate name. The policy is moved only when it is on the wrong side of that policy, so the rest of the order is left alone. When the other policy does not exist, the resource reports `ReferenceNotFound`.

### Who can open the firewall

Anyone who can create a `FirewallPolicy` in any namespace can open the firewall. Grant create, update and delete on `fortigate.technotut.net` resources only to cluster administrators and to Argo CD. The default Kubernetes `admin` and `edit` cluster roles do not include them, because the CRDs carry no aggregation labels.

## FortiGate setup

1. Create an admin profile that can change policies, addresses and services.

   ```
   config system accprofile
       edit "fortigate-operator"
           set fwgrp custom
           config fwgrp-permission
               set policy read-write
               set address read-write
               set service read-write
               set schedule read
           end
       next
   end
   ```

2. Create the REST API administrator and generate an API key. Restrict the trusted hosts to the addresses that pod traffic has after NAT, which are the node addresses.

   ```
   config system api-user
       edit "fortigate-operator"
           set accprofile "fortigate-operator"
           set vdom "root"
           config trusthost
               edit 1
                   set ipv4-trusthost 192.168.99.128 255.255.255.128
               next
           end
       next
   end
   execute api-user generate-key fortigate-operator
   ```

3. Allow HTTPS administrative access on the interface the pods reach the FortiGate through (for example `https` in `allowaccess` on `dmz`).

## Install

```sh
kubectl create namespace fortigate-operator
kubectl -n fortigate-operator create secret generic fortigate-operator --from-literal=token=<API key>
kubectl apply -k 'https://github.com/TechnoTUT/fortigate-operator//config/default?ref=main'
```

The Deployment in [config/manager](config/manager/manager.yaml) starts with `DRY_RUN=true`. Check in the logs and the resource status what would be written, then set it to `false` with a kustomize patch (see [config/default/kustomization.yaml](config/default/kustomization.yaml)).

### Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `FORTIGATE_URL` | (required) | For example `https://192.168.99.129` |
| `FORTIGATE_TOKEN` | | API key of the REST API administrator |
| `FORTIGATE_TOKEN_FILE` | | Read the API key from a file. Takes precedence over `FORTIGATE_TOKEN` |
| `FORTIGATE_TOKEN_IN_QUERY` | `false` | Send the key as the `access_token` query parameter instead of an `Authorization: Bearer` header |
| `FORTIGATE_VDOM` | `root` | |
| `FORTIGATE_CA_FILE` | | CA bundle (PEM) used to verify the FortiGate certificate |
| `FORTIGATE_INSECURE_SKIP_VERIFY` | `false` | Skip certificate verification, for a self-signed certificate |
| `DRY_RUN` | `false` | Log the changes and report them in the status instead of writing them |
| `RESYNC_INTERVAL` | `5m` | How often every resource is checked against the FortiGate |
| `METRICS_ADDR` | `:8080` | Prometheus metrics. `0` turns it off |
| `HEALTH_ADDR` | `:8081` | `/healthz` and `/readyz` |
| `LEADER_ELECT` | `false` | Enable leader election (needs `POD_NAMESPACE` and RBAC for leases) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

## Verification status

Checked:

- Unit tests against an in-memory FortiGate: creation, drift repair, set comparison of reference lists, policy positioning, ownership and adoption, deletion and orphaning, dry run.
- The CRDs and the RBAC pass server-side validation (dry run) on Kubernetes 1.37.
- Field names were taken from `show full-configuration firewall policy` on the production FortiGate-100D (FortiOS 6.2.16).

Not yet checked against a real FortiGate REST API:

- `PUT ...?action=move&before=<id>` and the `mkey` returned by `POST firewall/policy` on 6.2.
- Whether FortiOS changes `logtraffic` on a `deny` policy (this would show up as `NotConverged`).

## Development

```sh
make generate   # deepcopy code, CRDs and ClusterRole from the kubebuilder markers
make test
make image
```

Pushing to `main` or a `v*` tag makes GitHub Actions push the image to `ghcr.io/technotut/fortigate-operator`.
