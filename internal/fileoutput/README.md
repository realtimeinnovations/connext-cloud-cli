# Output policy

CLI output uses `fileoutput.WriteFile` (bytes), `Copy` (streams), or
`OpenAppend` (logs). Use `Validate` for preflight and `ValidateWithin` when a
generated path must stay inside an output directory. `Inspect` distinguishes
missing destinations from inspection failures; only missing paths are allowed
by preflight. `MkdirAll` applies the same policy to directory creation.

Resolve a user-selected directory with `ResolveDirectory` **before** joining
managed paths. This accepts symlinks at or above the chosen base, including
relocated project/output directories and the shared `.rti` directory. Missing
trailing directories are allowed; dangling links and non-directory bases fail.
For an explicit output file, `ResolveFile` resolves only its parent.

```text
exports -> /mnt/work/exports     allowed: user-selected base
  generated -> /elsewhere       rejected: generated child directory
  client.key -> /elsewhere/key  rejected: output file
```

Use the returned canonical path for subsequent preflight and writes. Never
resolve a generated subdirectory: that would hide a redirect below the base.
Writers require directory ancestors and regular-file destinations, rejecting
remaining symlinks and special files. The fixed macOS `/var`, `/tmp`, and `/etc`
aliases remain supported by strict validation for ordinary temporary paths.

Writers validate the destination and then use ordinary OS writes. They do not
stage files, rename destinations, create parents, or change permissions. Callers
keep their existing directory-creation and permission policies, including any
credential-specific chmod. New files respect the process umask on POSIX;
overwriting an existing file retains its inode and permissions. On Windows,
in-place writes preserve custom ACLs and work with readers that deny deletion.
Go's `0600` mode does not establish an owner-only Windows ACL.

Callers retain their overwrite, force, and editable-example preservation rules.
Validate an entire batch before writing when a known bad destination must block
all output, and preflight credential destinations before registration.
`common.DecodeSecureFiles` validates all server filenames and decodes the whole
response before saving it.

Specialized operations retain their publication contracts: installer locking
keeps its inode, migration uses no-clobber publication, downloads verify their
digest, license import and repair retain verified atomic replacement, and the
updater uses exclusive staging plus rollback. They use shared path validation for their destinations or,
for explicit license repair, their parent directories.

These checks are path-based, not a sandbox against concurrent ancestor swaps.
Preflight does not guarantee a later write will succeed. Multiple file writes
are not a transaction; interrupted writes can leave partial contents.

Filesystem edge cases belong in the utility's test matrix. Caller tests check
production wiring, permission selection, overwrite behavior, and whether
preflight precedes side effects; they should assert observable contents and
effects rather than helper call sequences.
