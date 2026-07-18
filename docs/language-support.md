# Language Support

This document separates semantic extraction support from deterministic filetype
inventory support. The `capabilities --json` output exposes both tiers:

- `supported_languages`: combined language/filetype names recognized by the
  provider.
- `semantic_languages`: languages with a parser-backed semantic extraction path.
- `inventory_only_languages`: filetypes recognized for stable file/document
  inventory, without claiming call/type/data-flow analysis.

Inventory-only coverage must not be counted as semantic-resolution parity in
competitive evaluations. It can count only for file discovery, document symbols,
and honest unsupported/degraded coverage reporting.

Current generated counts:

- Supported language/filetype names: 185
- Semantic languages: 36
- Inventory-only languages: 149

## Semantic Languages

- Bash
- C
- C#
- C++
- CUE
- Clojure
- ClojureScript
- Dart
- Elixir
- Erlang
- F#
- Go
- Groovy
- HCL
- Haskell
- Java
- JavaScript
- Julia
- Kotlin
- Lua
- OCaml
- Objective-C
- PHP
- Perl
- Protocol Buffers
- Python
- R
- Ruby
- Rust
- SQL
- Scala
- Swift
- TypeScript
- YAML
- Zig
- Zsh

Protocol Buffers support covers proto3 and legacy proto2 declarations,
including files that omit the syntax declaration, proto2 field labels, and
groups. Compatibility parsing preserves original source locations, signatures,
and hashes; genuinely malformed files still surface as partial failures.

## Inventory-Only Languages

- ABAP
- ANTLR
- ActionScript
- Agda
- AppleScript
- Arduino
- Assembly
- Astro
- Augeas
- AutoHotkey
- Awk
- BASIC
- BNF
- Babel Config
- Ballerina
- BibTeX
- Bicep
- Blade
- Boo
- Brainfuck
- Bundler
- CMake
- COBOL
- CSS
- Cabal
- Ceylon
- CoffeeScript
- ColdFusion
- Common Lisp
- Coq
- Crystal
- D
- DM
- DTD
- Dhall
- Diff
- Dockerfile
- Dotenv
- Dylan
- ECL
- ERB
- EditorConfig
- Eiffel
- Elm
- Factor
- Fantom
- Fish
- Forth
- GDB
- GDScript
- GLSL
- GameMaker Language
- Git Ignore
- Gnuplot
- GraphQL
- HTML
- Hack
- Haml
- Handlebars
- Homebrew Bundle
- INI
- Idris
- Io
- JSON
- JSON5
- JSP
- Jinja
- Just
- Kustomize
- LLVM
- Lean
- Less
- Lisp
- LiveScript
- M4
- MATLAB
- MCFunction
- MSBuild Project
- Make
- Markdown
- Mermaid
- Metal
- Mint
- MoonScript
- Mustache
- NPM Config
- Nim
- Nix
- Nu
- Objective-C++
- OpenSCAD
- Pascal
- Patch
- Pico-8
- Pip Requirements
- Pony
- Processing
- Property List
- Pug
- Puppet
- PureScript
- RON
- Racket
- Rake
- Raku
- Razor
- ReScript
- Reason
- Rego
- RubyGems
- SAS
- SCSS
- SPARQL
- SQF
- SRecode Template
- Sass
- Scheme
- ShaderLab
- Slim
- Solidity
- Stan
- Starlark
- Stylus
- Svelte
- TOML
- Tcl
- TeX
- Textile
- Thrift
- Twig
- V
- VCL
- VHDL
- Vala
- Vim Script
- Visual Basic .NET
- Vue
- VuePress
- WebAssembly
- Wolfram Language
- Wren
- XML
- XQuery
- XSLT
- Xtend
- Zimpl
- jq
- reStructuredText
- sed
