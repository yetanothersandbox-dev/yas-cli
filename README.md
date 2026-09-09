# yas

The command-line client for [YetAnotherSandbox](https://yetanothersandbox.dev) —
Firecracker microVMs on dedicated hardware that you can throw away and get back.
Root, Docker, SSH. Boots in less than a second, keeps everything you write to it, and
costs nothing while stopped.

## Install

```sh
brew install yetanothersandbox-dev/tap/yas
```

Or, with a Go toolchain:

```sh
go install github.com/yetanothersandbox-dev/yas-cli/cmd/yas@latest
```

Or download a binary from [Releases](https://github.com/yetanothersandbox-dev/yas-cli/releases).
macOS and Linux; the connect path is built on OpenSSH `ProxyCommand` mechanics
and Windows refuses at startup.

## Start

```sh
yas login          # one browser trip; your GitHub account is your account
yas new dev        # a box, and a shell in it
```

`yas` on its own lists every command, grouped by when you need it.

## What is in this repository

The client only. The control plane, the host agent and the guest runner live in
a separate repository — this one is public so that installing the CLI does not
require access to any of that.

It is a single static binary with no runtime dependencies except your own
`ssh`. Build it with `make yas`, test it with `make test`, and cross-compile a
release set with `make dist`.

## Documentation

[yetanothersandbox.dev/docs](https://yetanothersandbox.dev/docs) — the CLI, the
HTTP API, and the reference for both.
