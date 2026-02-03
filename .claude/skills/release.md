---
name: release
description: Create a new release by updating version files, committing, tagging, and pushing
user_invocable: true
arg: version (required, e.g., 1.2.3 or 1.2.3-rc1)
---

<release-skill>

# Release Skill

Create a new release for the Nova Scanner project. This skill updates all version references, creates a commit, tags it, and pushes to trigger the CD workflow.

## Instructions

When the user invokes `/release <version>`:

1. **Validate the version format**
   - Must match semver: `X.Y.Z` or `X.Y.Z-suffix` (e.g., `1.2.3` or `1.2.3-rc1`)
   - If invalid, explain the correct format and stop

2. **Check for clean git state**
   - Run `git status --porcelain`
   - If there are uncommitted changes, warn the user and ask if they want to proceed
   - If on a branch other than `main`, warn the user

3. **Update version files using yq**
   - Update `charts/nova-scanner/Chart.yaml`:
     - Set `version` to the version (without `v` prefix)
     - Set `appVersion` to `"vX.Y.Z"` (with `v` prefix, quoted)
   - Update `deploy/cronjob.yaml`:
     - Set the image tag to `vX.Y.Z`

4. **Show the diff for review**
   - Run `git diff` to show what changed
   - Ask the user to confirm the changes look correct

5. **Commit the changes**
   - Stage the modified files: `git add charts/nova-scanner/Chart.yaml deploy/cronjob.yaml`
   - Commit with message: `release: vX.Y.Z`

6. **Create an annotated tag**
   - Create tag: `git tag -a vX.Y.Z -m "Release vX.Y.Z"`

7. **Push to remote**
   - Push commit: `git push`
   - Push tag: `git push origin vX.Y.Z`

8. **Provide next steps**
   - Tell the user to monitor the CD workflow: `gh run list --workflow=cd.yaml --limit=1`
   - Remind them to verify after completion:
     - `gh release view vX.Y.Z`
     - `docker pull ghcr.io/olohmann/nova-automated-cluster-scanner:vX.Y.Z`
     - `helm repo update && helm search repo nova-scanner --versions | grep X.Y.Z`

## Example Commands

```bash
# Update Chart.yaml with yq
yq -i '.version = "1.2.3"' charts/nova-scanner/Chart.yaml
yq -i '.appVersion = "v1.2.3"' charts/nova-scanner/Chart.yaml

# Update cronjob.yaml with yq
yq -i '.spec.jobTemplate.spec.template.spec.containers[0].image = "ghcr.io/olohmann/nova-automated-cluster-scanner:v1.2.3"' deploy/cronjob.yaml
```

## Prerelease Detection

A version is considered a prerelease if it contains a hyphen after the version number (e.g., `1.2.3-rc1`, `1.2.3-beta`). Prereleases:
- Are marked as prerelease on GitHub
- Do NOT receive the `latest` Docker tag

</release-skill>
