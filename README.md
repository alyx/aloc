# aloc

A fast, parallel lines-of-code counter in the spirit of `cloc`, `scc`, and
`tokei` — with **smart, language-aware exclusion** of dependency and build
directories as its headline feature.

```
$ aloc
----------------------------------------------------------------------
Language                     Files       Blank     Comment        Code
----------------------------------------------------------------------
Go                              13         198         186        1786
Markdown                         2          64           0         353
----------------------------------------------------------------------
Total                           15         262         186        2139
----------------------------------------------------------------------
```

## Install

```sh
go build -o aloc ./cmd/aloc
```

## Usage

```
aloc [flags] [path ...]        # default path: .
```

| Flag | Description |
|---|---|
| `-f, --format <name>` | `table` (default), `json`, or `yaml` |
| `-o, --output <file>` | write to a file instead of stdout |
| `-e, --exclude <pat>` | exclude paths (repeatable, see below) |
| `-i, --include <pat>` | count only matching paths (repeatable) |
| `--ext go,py` | count only these extensions |
| `-l, --lang Go,Python` | count only these languages |
| `--by-file` | per-file detail |
| `--no-smart` | disable smart ecosystem exclusion |
| `--no-detect node,rust` | disable individual detectors |
| `--no-gitignore` | don't respect `.gitignore` files |
| `--tracked` | count only files tracked by git (see below) |
| `--git` | count tracked files using git objects for clean content (see below) |
| `--dedup` | count only one copy of files with identical content |
| `--hidden` | count hidden files/directories |
| `--follow-symlinks` | follow symlinks (loop-safe) |
| `-j, --jobs <n>` | parallel workers (default: CPU count) |
| `--config <file>` / `--no-config` | config file control |
| `--list-languages` / `--list-detectors` | show what's built in |
| `-v, --verbose` | warnings + applied smart exclusions on stderr |
| `-vv` | explain **every** skip decision: gitignore rule (with source file), exclude pattern, hidden, symlink, filters, unknown language (implies `-v`) |
| `-vvv` | also list every counted file and its detected language, after all filters (implies `-vv`) |

## Smart exclusion

`aloc` recognizes project ecosystems from marker files during traversal and
skips their dependency/build directories automatically — scoped to the marked
subtree, so monorepos behave:

```
$ aloc -v
aloc: smart-excluded api/vendor (detector: composer)
aloc: smart-excluded py/__pycache__ (detector: python)
aloc: smart-excluded py/my-env (detector: venv)
aloc: smart-excluded svc/target (detector: rust)
aloc: smart-excluded web/node_modules (detector: node)
...
```

Two mechanisms:

* **Markers**: a file that marks a directory as a project root of some
  ecosystem; the detector's directories are then excluded anywhere in that
  subtree.
* **Self-markers**: a file that marks its *containing* directory as
  disposable, whatever the directory is called — so a virtualenv named
  `my-weird-env/` is still caught.

| Detector | Ecosystem | Recognized by | Excludes |
|---|---|---|---|
| `node` | JavaScript / TypeScript (npm, yarn, pnpm) | `package.json` | `node_modules/`, `.pnpm-store/`, `bower_components/` |
| `composer` | PHP | `composer.json` | `vendor/` |
| `python` | Python | `pyproject.toml`, `setup.py`, `setup.cfg`, `requirements.txt`, `Pipfile` | `__pycache__/`, `.venv/`, `venv/`, `.tox/`, `.nox/`, `.mypy_cache/`, `.pytest_cache/`, `.ruff_cache/`, `.eggs/`, `*.egg-info/` |
| `venv` | Python virtualenvs | `pyvenv.cfg` inside any directory | that directory itself |
| `rust` | Rust (Cargo) | `Cargo.toml` | `target/` |
| `go` | Go (module vendoring) | `modules.txt` inside a dir named `vendor` | that `vendor/` directory |
| `maven` | Java (Maven) | `pom.xml` | `target/` |
| `gradle` | Java / Kotlin / Android (Gradle) | `build.gradle`, `build.gradle.kts`, `settings.gradle`, `settings.gradle.kts` | `build/`, `.gradle/` |
| `ruby` | Ruby (Bundler) | `Gemfile` | `.bundle/`, `vendor/bundle/` |
| `dotnet` | C# / F# (.NET) | `*.sln`, `*.csproj`, `*.fsproj` | `bin/`, `obj/` |
| `elixir` | Elixir (Mix) | `mix.exs` | `_build/`, `deps/`, `.elixir_ls/` |
| `dart` | Dart / Flutter | `pubspec.yaml` | `.dart_tool/`, `build/` |
| `swift` | Swift (SwiftPM) | `Package.swift` | `.build/` |
| `terraform` | Terraform | `*.tf` | `.terraform/` |
| `cmake` | C / C++ (CMake) | `CMakeCache.txt` inside any directory | that build directory itself |
| `zig` | Zig | `build.zig` | `zig-cache/`, `.zig-cache/`, `zig-out/` |
| `haskell` | Haskell (Stack / Cabal) | `stack.yaml`, `*.cabal` | `.stack-work/`, `dist-newstyle/` |

`--list-detectors` prints the same information for the running binary,
including any custom detectors from your config.

Every applied exclusion is recorded in the `excluded` array of JSON/YAML
output and printed to stderr with `-v` — including directories the project
also gitignores (smart detection is checked first, so `node_modules` is
attributed to the `node` detector even when `.gitignore` lists it). Disable everything with `--no-smart`,
individual detectors with `--no-detect`, or add your own in the config file.

VCS metadata (`.git`, `.hg`, `.svn`, `.bzr`) is always skipped.

## Pattern semantics

Rules for `--exclude`/`--include`, checked in this order:

1. **Explicit relative path** — starts with `./`: anchored at the scan root,
   matches exactly that subtree. `./project/thing` matches
   `project/thing/**` but never `foo/bar/project/thing`.
2. **Path glob** — contains `/`: also anchored at the scan root.
   `src/**/gen` matches any `gen` subtree under `src`.
3. **Broad name** — no `/`: matches any single path component anywhere.
   `foobar` excludes `project/thing/blah/foobar/file.js`; `*.min.js`
   excludes by basename.

`*`, `?`, and `[...]` never cross a `/`; `**` matches any number of path
segments (including zero). Matching a directory matches its whole subtree.
Excludes always win over includes. When multiple roots are given, patterns
are evaluated relative to each root.

All `-v`/`-vv`/`-vvv` diagnostics go to **stderr**, terminal or not, so
stdout is always just the report — `aloc -f json -vvv . | jq` works, and
`2> decisions.log` captures the trace separately.

Defaults: hidden files/directories are skipped (`--hidden` to include),
`.gitignore` files are respected with full semantics — anchoring, `!`
negation, `dir/`-only rules, `**`, nested files (`--no-gitignore` to
disable). Binary files are detected by NUL sniffing and skipped. Symlinks are
not followed unless `--follow-symlinks` is set, and are cycle- and
duplicate-safe when it is.

### `--tracked`

`--tracked` restricts counting to files in the git index (`git ls-files`) of
each path's repository — a hard filter applied **before** smart exclusion, so
untracked trees are pruned outright while a *committed* `vendor/` or
`node_modules/` still gets smart-excluded and attributed to its detector.
Each scan path may belong to a different repository; scanning a subdirectory
uses only that subtree of its repo. A path outside any git repository (or a
missing `git` binary) is a hard error.

It composes with the other rules as a pure filter: hidden files (`.github/`)
are still skipped unless you add `--hidden`, and excludes/includes still
apply. Git submodule contents are not counted — they belong to their own
repository's tree.

### `--git`

`--git` applies the same tracked-file filter as `--tracked`, but reads clean
files from the git object store through a single streaming `git cat-file`
process per repository. Modified and conflicted files are read from the
working tree, so its report remains identical to `--tracked`; untracked files
remain excluded. This substantially reduces per-file filesystem calls on
large repositories.

### `--dedup`

`--dedup` counts only one copy of byte-identical files, so copy-pasted or
generated duplicates don't inflate the numbers. Files are hashed with
SHA-256 (hardware-accelerated on amd64/arm64, faster than MD5 in Go, and
collision-safe) while they are counted, so the extra cost is one hash pass
over content already in memory. Deduplication is by content alone — it
crosses languages, and empty files collapse to one. The surviving copy is
always the lexicographically first path, keeping output deterministic;
`-vv` traces each casualty as `skip b.go (duplicate of a.go)`.

## Config file

Searched in order: `--config <file>`, `./.aloc.yml`, `./.aloc.yaml`,
`$XDG_CONFIG_HOME/aloc/config.yml` (falling back to
`~/.config/aloc/config.yml`). First found wins; CLI flags override.

```yaml
format: table
smart_exclude: true
gitignore: true
tracked: false
git: false
dedup: false
hidden: false
follow_symlinks: false
by_file: false
jobs: 0                  # 0 = CPU count
exclude: [fixtures, ./legacy/gen]
include: []
extensions: []
languages: []

detectors:
  disable: [node]
  custom:
    - name: mytool
      markers: [mytool.lock]     # project marker files (globs OK)
      exclude: [.mytool-cache]   # dirs excluded in that subtree
      # self_markers: [cache.tag]  # or: mark the containing dir itself
      # self_name: cache           # ...only when it has this basename

definitions:             # add or override languages
  FooLang:
    extensions: [foo]
    filenames: [Foofile]
    shebangs: [foorun]
    line_comments: ["#"]
    block_comments: [["<<", ">>"]]
    quotes: [['"', '"']]
    multiline_quotes: []
    raw_quotes: []
    nested_comments: false
```

## Machine-readable output

`aloc -f json` / `-f yaml` emit the same schema; languages are sorted by code
descending (name as tiebreak), files by path, so output is deterministic:

```json
{
  "schema_version": 1,
  "languages": [
    {"name": "Go", "files": 12, "lines": 3810, "blank": 234, "comment": 120, "code": 3456}
  ],
  "totals": {"files": 12, "lines": 3810, "blank": 234, "comment": 120, "code": 3456},
  "excluded": [{"path": "web/node_modules", "detector": "node"}]
}
```

With `--by-file`, each language gains a `files_detail` array. A new format is
one `func(io.Writer, *report.Report) error` entry in the `formatters` map in
`internal/output`.

## Counting rules

* A line is **blank** if it contains only whitespace — even inside a block
  comment or string.
* A line is **comment** if its non-blank content is entirely comment.
* Everything else is **code**; a line mixing code and comment counts as code.

The counter tracks block comments (with nesting where the language supports
it — Rust, Haskell, Kotlin, …) and string literals across lines, so comment
markers inside strings (`"// not a comment"`) and strings inside comments
don't miscount. Python docstrings count as code (they are string
expressions), matching `tokei`'s default. Known limitation: regex literals
(e.g. `/a\/\/b/` in JavaScript) can occasionally hide or fake a comment
marker — the same trade-off `cloc`/`scc`/`tokei` make.

Language detection order: exact filename (`Makefile`, `Dockerfile`,
`CMakeLists.txt`, …), then extension (case-insensitive), then shebang
(`#!/usr/bin/env python3`, versioned interpreters, `env -S`). Only files
with **no extension** are opened for the shebang check, matching `tokei` and
`scc` — a file with an unrecognized extension is skipped without a read. A
leading dot is not an extension separator, so `.bashrc` still gets the
shebang check (reachable with `--hidden`), as does a name with a bare
trailing dot (`file.`).

## Development

```sh
go test ./...        # full suite, includes end-to-end CLI tests
go test -race ./...
```

## License

[MIT](LICENSE)
