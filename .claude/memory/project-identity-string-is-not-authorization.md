---
name: project-identity-string-is-not-authorization
description: Never derive privilege from a name string a user can register — reserve privileged identity names at creation.
type: project
---

In shrt's token auth (PR #15, 2026-08-28), `adminOnly` trusted the identity
string `"admin"` — but member tokens were named by the admin at issue time, so
a member token literally named `admin` would have passed the gate.

**Why:** an identity STRING is data; authorization must key on which
CREDENTIAL matched (env admin token vs member store), or the privileged name
must be unregistrable. We fixed it by reserving `admin` at Issue time.

**How to apply:** when adding any named principal to a vybava service, either
carry the privilege level as a separate field resolved from the credential
source, or ban privileged names in the same commit that introduces them —
with a store-level and an API-level test.
