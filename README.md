# cxctl

`cxctl` is the command-line tool for [CloudXino](https://cloudxino.com). Use it to manage CC2 servers, Cloud Firewalls, load balancers, hosted DNS and SSH keys. It is built on the [CloudXino Go SDK](https://github.com/cloudxino/cloudxino-go).

```console
$ cxctl auth login
API key: ********
Logged in to Acme as "dev@example.com" (role developer); saved as "default" in ~/.config/cxctl/config.json.

$ cxctl ssh-key add --name laptop --public-key-file ~/.ssh/id_ed25519.pub
$ cxctl server create --name web-1 --image debian-13 --size s-1vcpu-1gb --ssh-key laptop --wait
$ cxctl server list
ID        NAME    STATUS    SIZE          REGION    IP             PRIVATE IP   TAGS
3f2c...   web-1   running   s-1vcpu-1gb   dhaka-1   203.0.113.20   10.0.0.5
```

## Install

Download a binary for your platform from the [releases](https://github.com/cloudxino/cxctl/releases), or build it with Go:

```sh
go install github.com/cloudxino/cxctl@latest
```

## Log in

Make an API key in the console under **Account → API keys**. Then use one of these:

- `cxctl auth login` reads the key from standard input, checks it, and saves it in `~/.config/cxctl/config.json` with mode 0600. Pipe the key in for scripts: `echo "$KEY" | cxctl auth login --name ci`.
- `--name` saves several logins, for example one per team. Switch between them with `cxctl auth use NAME`, or pick one for a single command with `--context NAME`.
- `CLOUDXINO_TOKEN` (or `--token`) overrides the saved login. This suits CI.

The key's role limits what cxctl can do: a `viewer` key can only read. Servers and load balancers are billed from the team's wallet.

## Commands

| | |
| --- | --- |
| `cxctl server list \| get \| create \| update \| delete` | CC2 servers |
| `cxctl server start \| stop \| reboot \| resize` | power and size; add `--wait` to wait until done |
| `cxctl server sizes \| regions \| images` | what `server create` accepts, with monthly prices |
| `cxctl server private-key` | the key the platform made for a server created without `--ssh-key` |
| `cxctl firewall list \| get \| create \| update \| delete` | Cloud Firewalls |
| `cxctl lb list \| get \| create \| update \| delete \| add-target \| remove-target` | load balancers |
| `cxctl dns zone list \| get \| create \| delete \| export \| import` | hosted DNS zones (BIND files for export and import) |
| `cxctl dns record list \| create \| update \| delete` | records in a zone |
| `cxctl reserved-ip list \| get \| create \| assign \| unassign \| delete` | reserved IPs: public IPv4 addresses the team keeps and points at a server; `assign` also moves one |
| `cxctl ssh-key list \| get \| add \| rename \| delete` | saved SSH public keys |
| `cxctl project list \| get`, `cxctl account` | projects; the team and role of the key |

`cxctl COMMAND --help` shows the flags and examples for each command.

Resources can be named by ID or by name: `cxctl server get web-1`. When two resources share a name, cxctl lists their IDs and asks for one. SSH keys can also be named by fingerprint, reserved IPs by address: `cxctl reserved-ip assign 203.0.113.10 web-2`.

### Rules

Firewall rules use the form `PROTOCOL:PORTS:ADDRESSES`. When you leave out the addresses, the rule allows any address:

```sh
cxctl firewall create --name web --tag web \
  --inbound tcp:80,443 \
  --inbound tcp:22:203.0.113.7/32 \
  --inbound icmp:0.0.0.0/0
```

Load-balancer rules use the form `ENTRY_PROTOCOL:ENTRY_PORT:TARGET_PORT`, for example `http:80:8080`. A health check uses the form `PROTOCOL[:PORT[:PATH]]`:

```sh
cxctl lb create --name front --rule http:80:8080 --health-check http:8080:/healthz \
  --target web-1 --target web-2 --wait
```

On `update`, each list you give (`--inbound`, `--tag`, `--rule`, `--target`) replaces the old list. Lists you leave out stay as they are.

## Scripting

- `-o json` prints what the API returned. `-o id` prints only IDs, one per line. `--no-header` drops the table header.
- Progress messages go to standard error, so standard output stays clean for parsing.
- Exit codes: `0` means success, `1` means the API or the operation failed, and `2` means the command line was wrong.
- Deleting asks for confirmation. Pass `--yes` when no one is at the terminal; without it, a run with no terminal stops instead of guessing.
- `server create --idempotency-key KEY` makes a re-run safe: running it again with the same key returns the first server instead of creating a second one.

```sh
for id in $(cxctl server list -o id --project Staging); do cxctl server stop "$id"; done
```

Shell completion: `cxctl completion bash|zsh|fish|powershell --help`.

## Developing

```sh
go test ./...   # every command runs against an in-memory fake API; nothing real is touched
go run . --help
```

Releases are made by pushing a `v*` tag. GoReleaser then builds the binaries for the GitHub release.

## License

[Apache-2.0](LICENSE)
