# ingressgen

`ingressgen` turns a project-owned YAML manifest into a deterministic,
default-deny `iptables-restore` file for one chain. It exists so Docker ingress
policy is reviewed as data and generated output cannot silently drift.

```sh
ingressgen render docker-ingress.yaml -o docker-user.rules
ingressgen check docker-ingress.yaml docker-user.rules
sudo ingressgen apply docker-ingress.yaml
```

Each manifest rule is an argument array, never a shell string. The renderer
accepts only `DOCKER-USER`, rejects whitespace/quoting injection, every chain
management option, duplicate rules, unconditional fail-open jumps, and a
ruleset whose final rule is not exactly `-j DROP`.

```yaml
schema_version: 1
chain: DOCKER-USER
rules:
  - comment: Keep replies to established connections
    args: [-m, conntrack, --ctstate, 'ESTABLISHED,RELATED', -j, RETURN]
  - comment: Fail closed
    args: [-j, DROP]
```

The generated file deliberately owns only `DOCKER-USER`. Never feed this
partial table file to bare `iptables-restore`: its default behavior flushes the
whole filter table. `ingressgen apply` is the supported application path; it
first syntax-checks and then applies with mandatory `--noflush` both times.
Host INPUT, OUTPUT, FORWARD, UFW, and Docker structural chains therefore remain
under their existing owners.
