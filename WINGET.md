# Publishing to winget

domping is published to [winget](https://learn.microsoft.com/windows/package-manager/)
as `SBroekhoven.domping`. Releases are automated: pushing a `vX.Y.Z` tag runs
[.github/workflows/release.yml](.github/workflows/release.yml), which uses
GoReleaser ([.goreleaser.yaml](.goreleaser.yaml)) to:

1. build binaries for Linux, macOS and Windows (amd64 and arm64),
2. publish a GitHub release with the archives and `checksums.txt`,
3. open a pull request on [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs)
   with the new manifest.

On Windows, winget unpacks the release zip and puts `domping` on PATH.

## One-time setup

1. Fork `microsoft/winget-pkgs` to `sbroekhoven/winget-pkgs`.
2. Create a GitHub token that can push to that fork and open pull requests.
   A classic token with the `public_repo` scope works.
3. Add the token to the domping repository as the Actions secret
   `WINGET_GITHUB_TOKEN` (Settings → Secrets and variables → Actions).

## Releasing

```sh
make snapshot              # optional local dry run into dist/, publishes nothing
git tag v0.2.1
git push origin v0.2.1
```

Then check:

- the **release** workflow run under the repository's Actions tab,
- the assets on the GitHub release,
- the pull request opened on `microsoft/winget-pkgs`.

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
