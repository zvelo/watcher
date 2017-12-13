# watcher

A simple tool that wraps the execution of another executable. If that executable changes on disk, then watcher will restart it with the same environment and arguments.

This is useful, in particular, for Go executables (which are often a single file, without external runtime file dependencies).

This is also useful when using a tool like [Foreman](https://github.com/ddollar/foreman) or [forego](https://github.com/ddollar/forego) which manage processes.

By wrapping each process's execution with `watcher`, a `Procfile` with lots of processes doesn't have to bring down every process each time a single executable changes.
