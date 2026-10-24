# TLS group preferences

The operator applies APIServer TLS profile groups to its authenticated metrics
and webhook servers, and renders decimal `--tls-curve-preferences` values for
compatible managed consumers. Empty custom groups leave Go defaults unchanged
and omit the operand flag. Named profiles use their API-defined groups.

The operator flag accepts comma-separated numeric Go CurveIDs. Any nonempty
minimum-version, cipher-suite, or curve option replaces the entire cluster
profile and disables its restart watcher. Invalid, duplicate, out-of-range,
and unknown numeric IDs are rejected. Unsupported API group names are logged
and omitted by the shared OpenShift helper. TLS 1.3 cipher settings remain
omitted independently of groups. Groups-only profile updates restart the
operator; changed rendered pod templates then roll supported consumers.

| API group | Numeric ID |
| --- | --- |
| X25519 | 29 |
| secp256r1 | 23 |
| secp384r1 | 24 |
| secp521r1 | 25 |
| X25519MLKEM768 | 4588 |
| SecP256r1MLKEM768 | 4587 |
| SecP384r1MLKEM1024 | 4589 |

Configured order is retained, but Go controls negotiation preference and its
native FIPS policy. No second operator-local FIPS allowlist is applied;
plain X25519 is filtered at handshake time under Go 1.26 FIPS policy.

## Pinned consumer compatibility

The compile-time gates in `pkg/config/tls_capabilities.go` are based on the
amd64 payload `5.1.0-0.nightly-2026-10-07-133149`, digest
`sha256:557b844bec22a806ea47b62e1ae0f62f8fc4055c816ab65d0bdb532fdcd2ac42`.
Public release metadata for this snapshot maps exact component digests to
source commits. Module and flag-registration
source at those commits supports the decisions below. This is source evidence:
no image binaries, startup, rollout, or handshake checks were executed in this
audit. It is not a release acceptance recommendation.

| Consumer | Exact source | Go / Kubernetes | Curves |
| --- | --- | --- | --- |
| AWS CCM Deployment | [e44d1350940f](https://github.com/openshift/cloud-provider-aws/tree/e44d1350940f8fcc82fd60f91c5984d7952a3b08) | 1.26.0 / k8s.io/cloud-provider and component-base v0.37.0; apiserver v0.37.0 | Enabled |
| Azure CCM Deployment | [2b2ba3b799d7](https://github.com/openshift/cloud-provider-azure/tree/2b2ba3b799d7e126826d63dc153611b97cced61f) | 1.26.0 / apiserver/cloud-provider/component-base v0.36.2 | Enabled |
| Azure Stack CCM Deployment | [2b2ba3b799d7](https://github.com/openshift/cloud-provider-azure/tree/2b2ba3b799d7e126826d63dc153611b97cced61f) | 1.26.0 / apiserver/cloud-provider/component-base v0.36.2 | Enabled |
| GCP CCM Deployment | [51c326465b31](https://github.com/openshift/cloud-provider-gcp/tree/51c326465b3160124b8097953b42e44f1056da5a) | 1.26.3 / apiserver/cloud-provider/component-base v0.36.2; kubernetes v1.36.0 | Enabled |
| IBM CCM Deployment | [11bc35dd6fd5](https://github.com/openshift/cloud-provider-ibm/tree/11bc35dd6fd5163259023a6f1dfcc59ce813ab5f) | 1.26.0 / cloud-provider/component-base v0.36.2; apiserver v0.36.2 indirect | Enabled |
| Nutanix CCM Deployment | [eded4633cafe](https://github.com/openshift/cloud-provider-nutanix/tree/eded4633cafeaab62f01736ef6a6115077bebf7c) | 1.25.0 / cloud-provider/component-base v0.34.0; apiserver v0.34.0 indirect | Deferred |
| OpenStack CCM Deployment | [fb1be731410a](https://github.com/openshift/cloud-provider-openstack/tree/fb1be731410a5d4eb6b3a3692c9081856df72c10) | 1.26.0 / apiserver/cloud-provider/component-base v0.36.0; kubernetes v1.36.0 | Enabled |
| PowerVS CCM Deployment | [00d531b76259](https://github.com/openshift/cloud-provider-powervs/tree/00d531b76259818c99147218b91ea9a42e726ab3) | 1.26.0 / cloud-provider/component-base v0.35.3; apiserver v0.35.3 indirect | Deferred |
| vSphere CCM Deployment | [eb29de194594](https://github.com/openshift/cloud-provider-vsphere/tree/eb29de194594bad8e5bc572102f1008cb26655a7) | 1.26.0 / cloud-provider/component-base v0.37.0-beta.0; apiserver v0.37.0-beta.0 indirect | Enabled |
| Azure cloud-node-manager DaemonSet | [2b2ba3b799d7](https://github.com/openshift/cloud-provider-azure/tree/2b2ba3b799d7e126826d63dc153611b97cced61f) | 1.26.0 / apiserver/component-base v0.36.2 | Enabled |
| Azure Stack cloud-node-manager DaemonSet | [2b2ba3b799d7](https://github.com/openshift/cloud-provider-azure/tree/2b2ba3b799d7e126826d63dc153611b97cced61f) | 1.26.0 / apiserver/component-base v0.36.2 | Enabled |

All nine CCM templates include a conditional stanza. Nutanix (Kubernetes 1.34,
Go 1.25) and PowerVS (Kubernetes 1.35.3) lack the curve flag and therefore render
no curve argument, preserving their existing minimum-version/cipher flags.
A disabled secure port does not avoid unknown-flag failures during parsing.
Both Azure-family node managers independently receive minimum-version,
cipher-suite, and curve flags; credential init containers receive none.
Unknown consumers fail closed. There are no runtime registry probes or image
tag heuristics. No local kube-rbac-proxy image key or workload exists, so no
proxy wiring is added.

Re-enable deferred consumers only after their provider dependencies are upgraded
(Kubernetes >=1.36; Nutanix also needs Go 1.26), images are promoted to a newly
pinned payload, and exact source/flag evidence and tests are refreshed. Those
provider-repository upgrades are separate work.

## Remaining runtime validation

Local tests verify conversion, watcher callbacks, and deterministic rendering,
not process startup or TLS negotiation. Later authorized payload/cluster checks
must verify supported flag acceptance and deferred consumers remaining runnable,
operator metrics/webhook and active CCM/node-manager TLS negotiation, rollout
health, and nonempty-to-changed-to-empty profile transitions. PowerVS group
negotiation awaits its provider upgrade. FIPS handshake verification is separate.
