# AKS Changelogs

This file tracks AKS-specific changes carried by the `aks-dev` branch relative to `opensandbox-group/main`. Keep entries in reverse chronological order and include a pull request or commit reference plus a one-sentence description.

## Release process

1. Add new AKS-local changes under `Unreleased` as PRs merge into `aks-dev`.
2. When cutting an AKS dev snapshot, choose a tag in the form `aksdev/YYYYMMDD` (for example, `aksdev/20260726`).
3. Move the relevant `Unreleased` entries into a new section named for that tag.
4. Merge the changelog update, then create and push the tag from the desired `aks-dev` commit:

   ```bash
   git fetch origin
   git checkout aks-dev
   git pull --ff-only origin aks-dev
   git tag -a aksdev/20260726 -m "AKS dev 20260726"
   git push origin aksdev/20260726
   ```

5. Publish AKS images with the same AKS tag. Docker image tags cannot contain `/`, so image publishing sanitizes `aksdev/20260726` to `aksdev-20260726` when composing image tags.

## Unreleased

- _No unreleased AKS-local changes._

## aksdev/20260726

- [`2a4d013`](https://github.com/bahe-msft/OpenSandbox/commit/2a4d01355776d7714ef8888b8f910c95d1e10cd3) — Cross-compiled Go binaries before Docker packaging to avoid slow arm64 Go builds under QEMU.
- [`cdffbfe`](https://github.com/bahe-msft/OpenSandbox/commit/cdffbfe2787c4b9911cad72f8ea594b244dbc2b6) — Skipped code-interpreter and removed the Windows execd binary from AKS image publishing to reduce publish time.
- [#11](https://github.com/bahe-msft/OpenSandbox/pull/11) / [`2fc47c2`](https://github.com/bahe-msft/OpenSandbox/commit/2fc47c266df6fbb76a9ec3776a026a8c33065822) — Added a dedicated GHCR workflow for publishing AKS images with AKS tag-aware image tags.
- [#8](https://github.com/bahe-msft/OpenSandbox/pull/8) / [`a53e0f7`](https://github.com/bahe-msft/OpenSandbox/commit/a53e0f766bb728174df486f4272201a909e8f00e) — Added a PTY shell fallback so execd can start interactive sessions when `bash` is unavailable.
- [#7](https://github.com/bahe-msft/OpenSandbox/pull/7) / [`0f16ab8`](https://github.com/bahe-msft/OpenSandbox/commit/0f16ab85c5e6356ab8a3d313db546af665cc4375) — Synced the Kata guest filesystem before snapshot commits to improve image-committer consistency.
