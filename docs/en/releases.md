# Releases and automatic builds

The `Build` workflow accepts pushed `v1.0.1` / `V1.0.1` tags and GitHub Release `published` events, including prereleases. The tagged commit must contain this workflow; updating main does not repair an old tag's workflow.

```sh
git tag -a V1.0.1 <verified-commit> -m "Release V1.0.1"
git push origin refs/tags/V1.0.1
```

Alternatively, publish a GitHub Release with a new tag targeting the verified commit. Use one entry point per version. Runs for a tag are serialized and never cancel each other during publication. Creating a new tag from Releases can emit both push and published events. A later published run skips duplicate builds only after verifying a successful push build for the same tag/commit and all seven complete assets. Releases created by the workflow's own `GITHUB_TOKEN` do not recursively trigger builds.

After Go checks, frontend and six-platform binary builds, the Release uploads Linux amd64/arm64/armv7, Windows amd64 and macOS amd64/arm64 packages. A reusable HA test workflow gates publication and supplies the exact tested `lalmax_nvr.zip` artifact. Missing expected assets fail publication.

Tags containing `-`, such as `v1.0.1-rc.1`, create prereleases. Their Docker images do not replace stable major/minor or `latest` channels. Both v/V prefixes are normalized for Docker semantic versions.

Verify Actions, all seven release assets and binary `--version`, then test the actual packages. HACS uses this repository's releases: validate first installation and upgrades, preserving config/entity identity and media functionality. A standalone download script is not a successful UI installation test.

After publishing Docker, `Released container smoke` runs the released binary and bundled FFmpeg on amd64, arm64 and ARMv7 (QEMU where needed). It can also be dispatched manually with an existing release tag. This test authenticates with the repository's package token; it does not prove anonymous pull access. New GHCR packages default to private. An organization administrator must explicitly configure package visibility for public downloads; making a package public cannot be reversed to private. Check anonymous pulls separately.

Retry transient failures using failed-job reruns. For an existing tag containing the workflow, manually dispatch `Build` against the tag. A manual branch run builds without publishing. Prefer a new tag for code fixes instead of moving a public release tag.

References: [Release events](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release), [GITHUB_TOKEN trigger restrictions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow).
