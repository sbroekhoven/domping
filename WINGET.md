# Publishing to winget

domping is published to [winget](https://learn.microsoft.com/windows/package-manager/)
as `SBroekhoven.domping`. Releases are automated: pushing a `vX.Y.Z` tag runs
[.github/workflows/release.yml](.github/workflows/release.yml), which uses
GoReleaser ([.goreleaser.yaml](.goreleaser.yaml)) to:

1. build binaries for Linux, macOS and Windows (amd64 and arm64),
2. publish a GitHub release with the archives and `checksums.txt`,
3. push the new manifest to a `domping-<version>` branch on the fork
   `sbroekhoven/winget-pkgs` and open a pull request from it on
   [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs).

On Windows, winget unpacks the release zip and puts `domping` on PATH.

## One-time setup

1. Fork `microsoft/winget-pkgs` to `sbroekhoven/winget-pkgs`.
2. Create a classic token at https://github.com/settings/tokens/new with only
   the `public_repo` scope.
3. Add the token to the **domping** repository (not the fork) as the Actions
   secret `WINGET_GITHUB_TOKEN` (Settings → Secrets and variables → Actions).

A fine-grained token is not enough: it can be limited to the fork, but it
cannot open pull requests on `microsoft/winget-pkgs` (GitHub answers 403). The
classic token covers all your public repositories, so give it an expiry and
renew the secret when it runs out. If you would rather use a fine-grained token,
set `pull_request.enabled: false` in `.goreleaser.yaml` and open each pull
request by hand from the `domping-<version>` branch on the fork.

## Releasing

```sh
make snapshot              # optional local dry run into dist/, publishes nothing
git tag -a v0.3.2 -m "v0.3.2"
git push origin v0.3.2
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
