description
Bump Kubernetes and Go to new minor versions across the project. Use when upgrading k8s (e.g. from 1.35 to 1.36) and Go (e.g. from 1.25 to 1.26). Handles go.mod, go.work, openshift-tests modules, cloud providers, and vendoring.

argument-hint
<k8s-minor> <go-minor> (e.g. 1.36 1.26)

allowed-tools
Bash(go *) Bash(git *) Bash(grep *) Bash(find *) Bash(gh *) Bash(make *) Bash(curl *) Bash(cat *)

Bump Kubernetes and Go Versions
================================

Target versions: **$ARGUMENTS**

Follow the instructions in docs/development/bump-k8s-go.md exactly, using `$ARGUMENTS` as the target Kubernetes and Go minor versions.
