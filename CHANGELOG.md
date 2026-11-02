# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Trivy filesystem and image scans in CI, reported to the GitHub Security tab.
- Release process: version embedded in the binary and logged on startup,
  GitHub Releases with linux binaries and checksums.

### Security
- Build with Go 1.26.
- Provider API responses are read with a 32 MiB size cap.
- Example compose files run the container `read_only`, with all capabilities
  dropped and `no-new-privileges`.
