# Publishing to winget

domping is published to [winget](https://learn.microsoft.com/windows/package-manager/)
as `SBroekhoven.domping`. Releases are automated: pushing a `vX.Y.Z` tag runs
[.github/workflows/release.yml](.github/workflows/release.yml), which uses
GoReleaser ([.goreleaser.yaml](.goreleaser.yaml)) to:

1. build binaries for Linux, macOS and Windows (amd64 and arm64),
2. publish a GitHub release with the archives and `checksums.txt`,
3. push the new manifest to a `domping-<version>` branch on the fork
   `sbroekhoven/winget-pkgs`. The pull request to
   [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs) is opened
   by hand (see Releasing).

On Windows, winget unpacks the release zip and puts `domping` on PATH.

## One-time setup

1. Fork `microsoft/winget-pkgs` to `sbroekhoven/winget-pkgs`.
2. Create a fine-grained token (Settings → Developer settings → Personal
   access tokens → Fine-grained tokens) limited to `sbroekhoven/winget-pkgs`,
   with **Contents** and **Pull requests** set to Read and write.
3. Add the token to the **domping** repository (not the fork) as the Actions
   secret `WINGET_GITHUB_TOKEN` (Settings → Secrets and variables → Actions).

A fine-grained token cannot open pull requests on `microsoft/winget-pkgs`, so
`pull_request.enabled` is `false` in `.goreleaser.yaml`. To make that step
automatic, use a classic token with the `public_repo` scope instead (it covers
all your public repositories) and set it to `true`.

## Releasing

```sh
make snapshot              # optional local dry run into dist/, publishes nothing
git tag -a v0.3.2 -m "v0.3.2"
git push origin v0.3.2
```

Then:

1. Check that the **release** workflow run under the repository's Actions tab
   succeeded and that the GitHub release has its assets.
2. Open the pull request:
   `https://github.com/microsoft/winget-pkgs/compare/master...sbroekhoven:winget-pkgs:domping-<version>`
   (for example `domping-0.3.2`; GitHub also shows a "Compare & pull request"
   banner on the fork). Fill in the checklist in the PR template and submit.

winget's moderators review the first submission by hand, which usually takes a
few days. Later version updates are normally merged automatically once the
validation pipeline passes. After the merge:

```sh
winget install SBroekhoven.domping
```

## Testing a manifest locally (Windows)

`make snapshot` writes the manifests to
`dist/winget/manifests/s/SBroekhoven/domping/<version>/`. On a Windows machine:

```powershell
winget validate <manifest-dir>
winget settings --enable LocalManifestFiles   # once, as administrator
winget install --manifest <manifest-dir>
```

The snapshot manifest points at release URLs that do not exist yet, so a local
install only works for a version that has already been released.
