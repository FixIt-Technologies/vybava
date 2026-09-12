# Worktime activity source

Install with `vybava install worktime`. `worktime --json sample` reads a single
native observation; `worktime watch --interval 5s` streams JSON lines.

The macOS adapter reports timestamp, foreground application bundle ID,
idle seconds and source. `sample --window-context` additionally reads the
foreground window title for local-only matching; consumers must discard that
title after matching. It does not read keystrokes, document contents or browser
URLs. Missing window permission returns an explicit context warning while
preserving the native idle/application observation. A missing native source fails
visibly and never produces activity. Linux and Windows native sampling are
currently unsupported; injected native-command fixtures verify the parser on
Linux without pretending to observe a person there.

Vitrinka's `vitrinka time enable` owns consent, exclusions, task attribution,
private offline storage and authenticated upload. The sampler itself neither
stores observations nor contacts a server. Keep downstream attribution
conservative: a foreground application alone does not establish a task.
