# ingressgen

`ingressgen` turns a project-owned YAML manifest into a deterministic,
default-deny `iptables-restore` file for one chain. It exists so Docker ingress
policy is reviewed as data and generated output cannot silently drift.

```sh
ingressgen render docker-ingress.yaml -o docker-user.rules
ingressgen check docker-ingress.yaml docker-user.rules
```

Each manifest rule is an argument array, never a shell string. The renderer
rejects embedded newlines, chain-management arguments, duplicate rules, and a
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

The generated file deliberately owns only the declared chain. Host INPUT,
OUTPUT, FORWARD, UFW, and Docker structural chains remain under their existing
owners.
