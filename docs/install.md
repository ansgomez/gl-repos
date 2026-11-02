# Install

How to install gl-repos and how to check that a release download is genuine. Back to the [README](../README.md).

## Platforms and requirements

**Platforms:** Linux and macOS (Intel and ARM). Windows is not supported
(gl-repos relies on Unix process groups to stop clones cleanly).

**Requirements:** git 2.31 or newer. Optional: `lazygit` (`o`), `glab`
and/or `gh` logged in (clone, new, publish), [Ollama](https://ollama.com)
with `ollama pull gemma4:e2b` (`i`), and the coding agents you want to start with `t`.
On Linux, [gl-repos on Linux](linux.md) lists the package commands.

## From source

Go 1.26+: clone the repository and run `make install`
(or `go install .`) inside it, which puts `gl-repos` in
`$(go env GOPATH)/bin`; add that directory to your `PATH` if needed.
`make build` builds `./gl-repos` in place instead. The module path is
plain `gl-repos`, so `go install <url>@latest` does not work: build from
a clone.

```sh
git clone https://github.com/ansgomez/gl-repos.git gl-repos && cd gl-repos
make install                  # puts gl-repos in $(go env GOPATH)/bin
export PATH="$(go env GOPATH)/bin:$PATH"   # if that is not on PATH yet
gl-repos -version
```

## Release downloads

Once a release is published: archives, `.deb`
and `.rpm` packages on the project's Releases page. Each release has a
`checksums.txt`, signed with [Sigstore](https://www.sigstore.dev)
(keyless) by the release job, and an SBOM per archive. The release job
also records a build provenance attestation for every file listed in
`checksums.txt`. The archives also hold the README, the docs (`docs/*.md`),
the changelog, the license and `config.example.toml`.

## Verify a release

Check the
archive, then that the checksums were signed by this project's release
workflow:

```sh
sha256sum -c checksums.txt --ignore-missing          # macOS: shasum -a 256 -c checksums.txt --ignore-missing

# released on GitHub (.github/workflows/release.yml)
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/ansgomez/gl-repos/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

Never verify with `'.*'` for both: that accepts a signature by anyone.

The build provenance attestation can be checked with gh instead of, or
as well as, cosign (needs a gh login):

```sh
gh attestation verify gl-repos_<version>_linux_amd64.tar.gz --repo ansgomez/gl-repos
```
