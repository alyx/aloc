# Changelog

All notable changes to aloc are documented here. The project follows Semantic
Versioning.

## [Unreleased]

## [1.2.0] - 2026-07-12

### Added

- Expanded built-in coverage from 76 to 151 languages and standalone source
  formats, including major HDL, shader/GPU, Microsoft, systems, Lisp-family,
  infrastructure, scientific, and modern application ecosystems.
- Added macOS arm64/x86-64 and Windows x86-64 release archives alongside the
  existing Linux arm64/x86-64 builds.
- Added installed-module version detection so `go install ...@version`
  reports that version without custom linker flags.

### Changed

- Added adaptive Linux io_uring activation so small trees avoid ring setup.
- Continued oversized io_uring reads from the existing prefix instead of
  rereading the whole file.
- Added delayed read-only mappings and exact allocation for large files.
- Split macOS filesystem and counting concurrency while keeping tiny-file
  reads and parsing contiguous.
- Specialized comment-free scanning and packed short-extension lookup to
  retain throughput as the language registry grows.
- Lowered the documented and tested minimum toolchain to Go 1.22.

### Performance

- Retained the Linux 6.6 lead over loc and tokei on both tested arm64 and
  amd64 systems while recognizing at least as many files as tokei.
- Reduced the previous large-contiguous-file gap on amd64 to within 10% of
  loc in the benchmark corpus and improved many-small-file handling on macOS.

[Unreleased]: https://github.com/alyx/aloc/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/alyx/aloc/compare/v1.1.0...v1.2.0
